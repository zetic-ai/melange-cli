package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/fixturetest"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// scopedContext mints a context carrying info exactly the way production
// does: through the SDK's own RequireBearerToken middleware. Tests never
// touch the SDK's unexported context key, so this cannot drift from what the
// streamable HTTP stack actually gives tool handlers.
func scopedContext(t *testing.T, info *auth.TokenInfo) context.Context {
	t.Helper()
	var captured context.Context
	verifier := func(context.Context, string, *http.Request) (*auth.TokenInfo, error) {
		return info, nil
	}
	handler := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
		AllowMissingExpiration: true,
	})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = r.Context()
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer test-bearer")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	require.NotNil(t, captured, "RequireBearerToken must accept the fabricated bearer")
	return captured
}

// Discriminators of the scope refusal: assertions match on these, never on
// IsError alone, so no other failure mode can impersonate a scope refusal.
const (
	refusalNeedsWrite  = `needs the "write" scope`
	refusalReauthorize = `Re-authorize granting the "write" scope`
)

func TestScopeSatisfied(t *testing.T) {
	assert.True(t, scopeSatisfied([]string{"read"}, scopeRead))
	assert.True(t, scopeSatisfied([]string{"read", "write"}, scopeWrite))
	assert.True(t, scopeSatisfied([]string{"write"}, scopeWrite),
		"scopes arrive as granted; write alone must satisfy write")
	assert.True(t, scopeSatisfied([]string{"write"}, scopeRead),
		"write implies read: a write-only grant must satisfy a read requirement")
	assert.False(t, scopeSatisfied([]string{"read"}, scopeWrite))
	assert.False(t, scopeSatisfied(nil, scopeWrite))
}

func TestRequireScope(t *testing.T) {
	d := Deps{}

	assert.Nil(t, d.requireScope(context.Background(), scopeWrite),
		"no TokenInfo (the stdio transport) must disable enforcement")
	assert.Nil(t, d.requireScope(scopedContext(t, &auth.TokenInfo{}), scopeWrite),
		"an empty TokenInfo (PassthroughVerifier) must disable enforcement — the PR2 trap")
	assert.Nil(t, d.requireScope(scopedContext(t, &auth.TokenInfo{Scopes: []string{"write"}}), scopeRead),
		"write implies read through the gate")
	assert.Nil(t, d.requireScope(scopedContext(t, &auth.TokenInfo{Scopes: []string{"write"}}), scopeWrite))

	refusal := d.requireScope(scopedContext(t, &auth.TokenInfo{Scopes: []string{"read"}}), scopeWrite)
	require.NotNil(t, refusal)
	require.True(t, refusal.IsError)
	text := textOf(t, refusal)
	assert.Contains(t, text, refusalNeedsWrite)
	assert.Contains(t, text, `granted only "read"`)
	assert.Contains(t, text, refusalReauthorize, "the remediation must be agent-actionable")
	assert.NotContains(t, text, "test-bearer", "refusals never carry credential bytes")
}

// scopeToolCase drives one mutating tool's handler directly (bypassing the
// session, so the TokenInfo context can be injected) under the four scope
// postures.
type scopeToolCase struct {
	tool string
	// invoke calls the handler with valid arguments and returns its result.
	invoke func(t *testing.T, d Deps, ctx context.Context) *mcp.CallToolResult
	// stub registers the API responses the tool needs to succeed.
	stub func(t *testing.T, reg *httpmock.Registry)
}

// run invokes the case against a fresh registry and deps.
func (tc scopeToolCase) run(t *testing.T, ctx context.Context, stubbed bool) (*mcp.CallToolResult, *httpmock.Registry) {
	t.Helper()
	reg := &httpmock.Registry{}
	if stubbed {
		tc.stub(t, reg)
	}
	d := Deps{Clients: registryProvider(t, reg), Bare: &http.Client{Transport: reg}}
	return tc.invoke(t, d, ctx), reg
}

// scopeToolCases enumerates EVERY mutating tool. TestMutatingToolCasesCoverCatalog
// pins the list to the registered catalog, so a new mutating tool cannot ship
// without joining this table.
func scopeToolCases() []scopeToolCase {
	return []scopeToolCase{
		{
			tool: "create_repo",
			invoke: func(t *testing.T, d Deps, ctx context.Context) *mcp.CallToolResult {
				res, _, err := createRepoHandler(d)(ctx, nil, createRepoArgs{Name: "demo"})
				require.NoError(t, err)
				return res
			},
			stub: func(t *testing.T, reg *httpmock.Registry) {
				reg.Register(httpmock.REST("POST", "/v1/repos"), jsonBody(200, repoBody))
			},
		},
		{
			tool: "update_repo",
			invoke: func(t *testing.T, d Deps, ctx context.Context) *mcp.CallToolResult {
				desc := "updated"
				res, _, err := updateRepoHandler(d)(ctx, nil, updateRepoArgs{
					Repo: "zetic/whisper-tiny", Description: &desc,
				})
				require.NoError(t, err)
				return res
			},
			stub: func(t *testing.T, reg *httpmock.Registry) {
				reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"), jsonBody(200, repoBody))
			},
		},
		{
			tool: "delete_repo",
			invoke: func(t *testing.T, d Deps, ctx context.Context) *mcp.CallToolResult {
				res, _, err := deleteRepoHandler(d)(ctx, nil, deleteRepoArgs{
					Repo: "zetic/whisper-tiny", Confirm: "zetic/whisper-tiny",
				})
				require.NoError(t, err)
				return res
			},
			stub: func(t *testing.T, reg *httpmock.Registry) {
				reg.Register(httpmock.REST("DELETE", "/v1/repos/zetic/whisper-tiny"),
					httpmock.StatusStringResponse(204, ""))
			},
		},
		{
			tool: "set_default_model",
			invoke: func(t *testing.T, d Deps, ctx context.Context) *mcp.CallToolResult {
				res, _, err := setDefaultModelHandler(d)(ctx, nil, setDefaultModelArgs{
					Repo: "zetic/whisper-tiny", ModelKey: "m_1",
				})
				require.NoError(t, err)
				return res
			},
			stub: func(t *testing.T, reg *httpmock.Registry) {
				reg.Register(httpmock.REST("PUT", "/v1/repos/zetic/whisper-tiny/models/m_1/default"),
					jsonBody(200, `{"key":"m_1","is_default":true}`))
			},
		},
		{
			tool: "import_model",
			invoke: func(t *testing.T, d Deps, ctx context.Context) *mcp.CallToolResult {
				res, _, err := importModelHandler(d)(ctx, nil, importModelArgs{
					Repo: "zetic/whisper-tiny", HfRepo: "meta-llama/Llama-3.2-1B",
				})
				require.NoError(t, err)
				return res
			},
			stub: func(t *testing.T, reg *httpmock.Registry) {
				reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper-tiny/models/import"),
					jsonBody(200, `{"key":"m_2","state":"IMPORTING"}`))
			},
		},
		{
			tool: "request_model_download",
			invoke: func(t *testing.T, d Deps, ctx context.Context) *mcp.CallToolResult {
				res, _, err := requestModelDownloadHandler(d)(ctx, nil, requestModelDownloadArgs{
					Repo: "zetic/whisper-tiny", ModelKey: "m_1", TargetID: "t_1",
					Confirm: true, IncludeURLs: true,
				})
				require.NoError(t, err)
				return res
			},
			stub: func(t *testing.T, reg *httpmock.Registry) {
				reg.Register(httpmock.REST("POST",
					"/v1/repos/zetic/whisper-tiny/models/m_1/targets/t_1/download-authorizations"),
					jsonBody(200, `{"authorization_id":"da_1","expires_at":"2026-01-01T00:00:00Z","artifacts":[]}`))
			},
		},
		{
			tool: "upload_model",
			invoke: func(t *testing.T, d Deps, ctx context.Context) *mcp.CallToolResult {
				// Durable upload state/locks are isolated per test.
				stateHome := t.TempDir()
				t.Setenv("XDG_STATE_HOME", stateHome)
				t.Setenv("LOCALAPPDATA", stateHome)
				model, inputs := uploadFixtureLocalFiles(t)
				fx := fixturetest.Load(t, "create_model_upload")
				res, _, err := uploadModelHandler(d)(ctx, nil, uploadModelArgs{
					Repo: repoArg(t, fx), ModelFile: model, Inputs: inputs,
				})
				require.NoError(t, err)
				return res
			},
			stub: func(t *testing.T, reg *httpmock.Registry) {
				// The create fixture's response must echo the client_file_ids
				// THIS run generates ("f<position>"), exactly as the fixture
				// round-trips remap them.
				fx := fixturetest.Load(t, "create_model_upload")
				body := string(fixturetest.Concretize(fx.Response.Body))
				for i, id := range uploadFixtureClientIDs(t, fx) {
					body = strings.ReplaceAll(body, fmt.Sprintf("%q", id), fmt.Sprintf(`"f%d"`, i))
				}
				stubFixtureBody(reg, fx, body)
				registerUploadTransferStubs(reg)
				stubFixture(reg, fixturetest.Load(t, "complete_model_upload"))
			},
		},
	}
}

// TestMutatingToolsEnforceWriteScope is the per-tool enforcement matrix.
// Removing the requireScope gate from any single handler fails that tool's
// "read scope refused" subtest, so enforcement is pinned tool by tool.
func TestMutatingToolsEnforceWriteScope(t *testing.T) {
	readCtx := scopedContext(t, &auth.TokenInfo{Scopes: []string{"read"}})
	// As granted: the authorization server may mint ["write"] without "read".
	writeCtx := scopedContext(t, &auth.TokenInfo{Scopes: []string{"write"}})
	passthroughCtx := scopedContext(t, &auth.TokenInfo{})

	for _, tc := range scopeToolCases() {
		t.Run(tc.tool, func(t *testing.T) {
			t.Run("read scope is refused with zero API calls", func(t *testing.T) {
				res, reg := tc.run(t, readCtx, false) // empty registry: any request would error loudly
				require.NotNil(t, res)
				require.True(t, res.IsError, "%s must refuse a read-only token", tc.tool)
				text := textOf(t, res)
				assert.Contains(t, text, refusalNeedsWrite)
				assert.Contains(t, text, refusalReauthorize)
				assert.Empty(t, reg.Requests, "a scope refusal must make zero API calls")
			})

			t.Run("write scope proceeds", func(t *testing.T) {
				res, reg := tc.run(t, writeCtx, true)
				require.NotNil(t, res)
				if res.IsError {
					t.Fatalf("%s must succeed with the write scope, got: %s", tc.tool, textOf(t, res))
				}
				assert.NotEmpty(t, reg.Requests, "the permitted call must reach the API")
			})

			t.Run("no TokenInfo (stdio) is unenforced", func(t *testing.T) {
				res, reg := tc.run(t, context.Background(), true)
				require.NotNil(t, res)
				if res.IsError {
					t.Fatalf("%s must be unaffected over stdio, got: %s", tc.tool, textOf(t, res))
				}
				assert.NotEmpty(t, reg.Requests)
			})

			t.Run("empty TokenInfo (passthrough) is unenforced", func(t *testing.T) {
				res, reg := tc.run(t, passthroughCtx, true)
				require.NotNil(t, res)
				if res.IsError {
					t.Fatalf("%s must be unaffected under PassthroughVerifier, got: %s", tc.tool, textOf(t, res))
				}
				assert.NotEmpty(t, reg.Requests)
			})
		})
	}
}

// TestMutatingToolCasesCoverCatalog pins the scope matrix to the registered
// catalog: every non-read-only tool in the full (local superset) catalog must
// have a case, and every case must name a registered tool. A new mutating tool
// cannot ship without scope enforcement coverage.
func TestMutatingToolCasesCoverCatalog(t *testing.T) {
	cases := map[string]bool{}
	for _, tc := range scopeToolCases() {
		cases[tc.tool] = true
	}

	cs := connectWith(t, "test", Options{EnableLocalTools: true})
	registered := map[string]bool{}
	for _, tool := range listAllTools(t, cs) {
		registered[tool.Name] = true
		require.NotNil(t, tool.Annotations, "%s must declare annotations", tool.Name)
		if !tool.Annotations.ReadOnlyHint {
			assert.True(t, cases[tool.Name],
				"mutating tool %s has no scope-enforcement case in scopes_test.go", tool.Name)
		}
	}
	for name := range cases {
		assert.True(t, registered[name], "scope case %s matches no registered tool", name)
	}
}

// TestRequiresWriteScopeMatchesCatalog pins the exported write-scope tool set
// to the registered catalog: RequiresWriteScope must be true for exactly the
// tools whose ReadOnlyHint is false (the local superset, so upload_model is
// covered). The HTTP transport's insufficient_scope gate keys on this set, so
// drift in either direction is a shipped bug — a missing mutating tool would
// silently skip the RFC 6750 signal (falling back to the in-band refusal),
// and a stale entry would 403 a tool that no longer needs write.
func TestRequiresWriteScopeMatchesCatalog(t *testing.T) {
	cs := connectWith(t, "test", Options{EnableLocalTools: true})
	seen := map[string]bool{}
	for _, tool := range listAllTools(t, cs) {
		require.NotNil(t, tool.Annotations, "%s must declare annotations", tool.Name)
		seen[tool.Name] = true
		assert.Equal(t, !tool.Annotations.ReadOnlyHint, RequiresWriteScope(tool.Name),
			"RequiresWriteScope(%s) must mirror the catalog's ReadOnlyHint", tool.Name)
	}
	for name := range writeScopeTools {
		assert.True(t, seen[name], "write-scope entry %s matches no registered tool", name)
	}
}

// TestWriteScopeRefusalTextMatchesToolError pins the byte-identity between the
// exported refusal text (the HTTP 403 body) and the in-band tool error, so
// the two layers can never teach an agent two different remediations.
func TestWriteScopeRefusalTextMatchesToolError(t *testing.T) {
	d := Deps{}
	refusal := d.requireScope(scopedContext(t, &auth.TokenInfo{Scopes: []string{"read"}}), scopeWrite)
	require.NotNil(t, refusal)
	assert.Equal(t, WriteScopeRefusalText([]string{"read"}), textOf(t, refusal))
}

// TestStdioSessionUnaffectedByScopeEnforcement pins the frozen stdio behavior
// end to end: over a real (in-memory, i.e. non-HTTP — the stdio code path)
// session there is no TokenInfo, so a mutating tool runs exactly as before
// scope enforcement existed.
func TestStdioSessionUnaffectedByScopeEnforcement(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos"), jsonBody(200, repoBody))
	cs, _ := connect(t, registryProvider(t, reg))

	res := callTool(t, cs, "create_repo", map[string]any{"name": "whisper-tiny"})
	if res.IsError {
		t.Fatalf("stdio create_repo must be unaffected by scope enforcement, got: %s", textOf(t, res))
	}
	require.Len(t, reg.Requests, 1, "the call must reach the API")
}
