package mcp

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the property the HTTP transport's schema caching stands on:
// the SDK's mcp.SchemaCache keys resolved schemas for explicitly provided
// schemas by POINTER identity (go-sdk v1.7.0 SchemaCache.getBySchema), so the
// one-server-per-request design only skips re-resolution if this package
// hands AddTool the very same schema pointer on every New. No wall-clock
// timing is asserted anywhere — pointer identity is the whole contract.

func TestOutputSchemaPointerStableAcrossCalls(t *testing.T) {
	assert.Same(t, outputSchema("whoami"), outputSchema("whoami"),
		"outputSchema must memoize: the SDK schema cache is keyed by pointer identity")
}

// TestOutputSchemaPointerStableUnderConcurrentFirstParse forces the cold path
// and races the first parse: every caller must converge on one pointer, and
// -race must observe no data race while they do.
func TestOutputSchemaPointerStableUnderConcurrentFirstParse(t *testing.T) {
	outputSchemas.Delete("get_repo")

	const callers = 16
	ptrs := make([]*jsonschema.Schema, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ptrs[i] = outputSchema("get_repo")
		}()
	}
	wg.Wait()
	for i, p := range ptrs {
		require.Same(t, ptrs[0], p, "caller %d observed a different schema pointer", i)
	}
}

func TestInputSchemaPointerStableAcrossCalls(t *testing.T) {
	assert.Same(t,
		inputSchemaFor[listReposArgs](withPageBounds),
		inputSchemaFor[listReposArgs](withPageBounds),
		"inputSchemaFor must memoize: the SDK schema cache is keyed by pointer identity")
}

// TestInputSchemaForRejectsConflictingRefine pins the memoization's fail-loud
// guard: an args type is its schema's identity, so registering it again under
// a different refinement is a programming error, not a silent reuse of the
// first caller's bounds.
func TestInputSchemaForRejectsConflictingRefine(t *testing.T) {
	_ = inputSchemaFor[listReposArgs](withPageBounds)
	assert.Panics(t, func() { _ = inputSchemaFor[listReposArgs](withWaitBounds) },
		"a second refine for the same args type must panic at registration")
}

// TestSharedSchemaCacheChangesNothingObservable builds servers without a
// cache, with a cold shared cache, and with that cache warm (the per-request
// steady state), and requires the advertised tool catalog to be byte-identical
// across all three. Schema caching may only change when resolution work
// happens, never what a client sees.
func TestSharedSchemaCacheChangesNothingObservable(t *testing.T) {
	listed := func(opts Options) string {
		cs := connectWith(t, "v-test", opts)
		tools, err := cs.ListTools(t.Context(), nil)
		require.NoError(t, err)
		out, err := json.Marshal(tools.Tools)
		require.NoError(t, err)
		return string(out)
	}

	cache := mcp.NewSchemaCache()
	uncached := listed(Options{})
	coldCache := listed(Options{SchemaCache: cache})
	warmCache := listed(Options{SchemaCache: cache})

	assert.Equal(t, uncached, coldCache, "a cold shared cache must not change the advertised catalog")
	assert.Equal(t, uncached, warmCache, "a warm shared cache must not change the advertised catalog")
}
