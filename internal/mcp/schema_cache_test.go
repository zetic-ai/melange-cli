package mcp

import (
	"encoding/json"
	"reflect"
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

// TestInputSchemaPointerStableUnderConcurrentFirstDerivation mirrors the
// output-schema race test onto inputSchemaFor: force the cold path, race the
// first derivation, require every caller to converge on one pointer with
// -race silent.
func TestInputSchemaPointerStableUnderConcurrentFirstDerivation(t *testing.T) {
	inputSchemas.Delete(reflect.TypeFor[listReposArgs]())

	const callers = 16
	ptrs := make([]*jsonschema.Schema, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ptrs[i] = inputSchemaFor[listReposArgs](withPageBounds)
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

// TestSharedSchemaCacheCutsConstructionAllocations guards the win itself: the
// pointer-identity and observable-equality tests above stay green even if
// Options.SchemaCache is silently dropped on the floor, so this test pins
// that a warm cache actually short-circuits schema resolution inside New.
// Allocation counting keeps it wall-clock-free and deterministic: an uncached
// New resolves every schema (~60k allocations), a warm-cache New skips all of
// it (~2k), so the loose 10x threshold has ~3x margin on both sides and fails
// the moment New stops handing opts.SchemaCache to the SDK.
func TestSharedSchemaCacheCutsConstructionAllocations(t *testing.T) {
	deps := Deps{Version: "alloc-guard"}
	cache := mcp.NewSchemaCache()
	New(deps, Options{SchemaCache: cache}) // warm, as the first HTTP request would

	baseline := testing.AllocsPerRun(5, func() { New(deps, Options{}) })
	warm := testing.AllocsPerRun(5, func() { New(deps, Options{SchemaCache: cache}) })

	assert.Less(t, warm, baseline/10,
		"a warm shared cache must skip schema resolution during New: %.0f allocs cached vs %.0f uncached",
		warm, baseline)
}
