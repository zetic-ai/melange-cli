package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// update regenerates the golden files under testdata/ instead of comparing
// against them:
//
//	go test ./internal/mcp -run TestToolsList -update
var update = flag.Bool("update", false, "rewrite golden files under testdata/")

// connectWith is connect for a caller that needs to pick the server version
// and Options; the shared connect helper pins Version "test", Options{}.
func connectWith(t *testing.T, version string, opts Options) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	srv := New(Deps{Clients: registryProvider(t, &httpmock.Registry{}), Version: version}, opts)
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Wait() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession
}

// TestInitializeHandshake pins what the server advertises during initialize:
// implementation name `melange`, the wired-in version verbatim, and the tools
// capability an MCP client keys feature discovery on.
func TestInitializeHandshake(t *testing.T) {
	cs := connectWith(t, "1.2.3-test", Options{})

	init := cs.InitializeResult()
	require.NotNil(t, init, "the session keeps the initialize result")
	require.NotNil(t, init.ServerInfo)
	assert.Equal(t, "melange", init.ServerInfo.Name)
	assert.Equal(t, "1.2.3-test", init.ServerInfo.Version,
		"Deps.Version flows into the handshake verbatim")
	require.NotNil(t, init.Capabilities)
	assert.NotNil(t, init.Capabilities.Tools, "the server advertises tools")
}

// listAllTools drains the tools/list pagination so the snapshot cannot
// silently lose tools to a page boundary.
func listAllTools(t *testing.T, cs *mcp.ClientSession) []*mcp.Tool {
	t.Helper()
	var tools []*mcp.Tool
	var cursor string
	for {
		res, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{Cursor: cursor})
		require.NoError(t, err)
		tools = append(tools, res.Tools...)
		if res.NextCursor == "" {
			return tools
		}
		cursor = res.NextCursor
	}
}

// renderTools renders a tools/list result the way the golden file stores it:
// two-space indent and no HTML escaping, so description prose like <redacted>
// stays readable and the file diffs cleanly.
func renderTools(t *testing.T, tools []*mcp.Tool) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	require.NoError(t, enc.Encode(tools))
	return buf.Bytes()
}

// TestToolsListGoldenSnapshot pins the full advertised catalog — every tool's
// name, description, annotations, and complete input and output schemas, as a
// real client receives them over the wire — against a golden file. Any change
// to the tool surface shows up as a reviewable diff to
// testdata/tools_list_stdio.json; regenerate deliberately with -update.
func TestToolsListGoldenSnapshot(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	tools := listAllTools(t, cs)
	require.NotEmpty(t, tools)

	// Stable ordering: the SDK lists tools sorted by name, so the snapshot is
	// deterministic. Assert it rather than assume it, and rule out duplicates.
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	assert.True(t, sort.StringsAreSorted(names), "tools/list is sorted by name: %v", names)
	for i := 1; i < len(names); i++ {
		assert.NotEqual(t, names[i-1], names[i], "duplicate tool name in tools/list")
	}

	rendered := renderTools(t, tools)
	golden := filepath.Join("testdata", "tools_list_stdio.json")
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o755))
		require.NoError(t, os.WriteFile(golden, rendered, 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err, "golden file missing; generate it with -update")
	assert.Equal(t, string(want), string(rendered),
		"tools/list drifted from the golden snapshot; if the change is intended, "+
			"regenerate with: go test ./internal/mcp -run TestToolsListGoldenSnapshot -update")
}

// TestEveryToolStatesItsBlastRadius holds the whole catalog to the
// explicit-annotation discipline for the two hints that default to true.
// A tool added later that omits them silently tells every agent it may be
// destructive and may reach arbitrary third-party systems, which is exactly
// the signal a human-in-the-loop client uses to decide whether to prompt.
// import_model is the only tool that genuinely leaves the Melange API.
func TestEveryToolStatesItsBlastRadius(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	for _, tool := range listAllTools(t, cs) {
		t.Run(tool.Name, func(t *testing.T) {
			require.NotNil(t, tool.Annotations, "%s has no annotations", tool.Name)
			require.NotNil(t, tool.Annotations.DestructiveHint,
				"%s leaves DestructiveHint at its true default", tool.Name)
			require.NotNil(t, tool.Annotations.OpenWorldHint,
				"%s leaves OpenWorldHint at its true default", tool.Name)
			assert.Equal(t, tool.Name == "import_model", *tool.Annotations.OpenWorldHint,
				"only import_model reaches outside the Melange API")
		})
	}
}

// TestEnableLocalToolsCatalogIsIdenticalToday asserts the EnableLocalTools
// variant advertises exactly the stdio catalog: no stdio-only tools exist yet,
// so a second golden file would be a byte-for-byte copy. The upload PR, which
// introduces the first local tool, must replace this equality with its own
// golden snapshot (e.g. testdata/tools_list_local.json).
func TestEnableLocalToolsCatalogIsIdenticalToday(t *testing.T) {
	stdio := renderTools(t, listAllTools(t, connectWith(t, "test", Options{})))
	local := renderTools(t, listAllTools(t, connectWith(t, "test", Options{EnableLocalTools: true})))
	assert.Equal(t, string(stdio), string(local),
		"EnableLocalTools changed the catalog: add a second golden file for the local variant")
}
