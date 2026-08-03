package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// Bodies use non-alphabetical key order so any re-marshal through a typed
// struct (which would sort keys) breaks the byte-equality assertions.
const (
	repoListBody = `{"results":[{"full_name":"zetic/whisper-tiny","visibility":"public","model_type":"onnx"}],"count":1}`
	repoBody     = `{"name":"whisper-tiny","visibility":"public","model_type":"onnx","description":"tiny"}`
)

// callTool runs one tool call over the in-memory transport.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err, "API and argument failures are tool errors, not protocol errors")
	return res
}

// toolNamed returns the advertised definition of one registered tool.
func toolNamed(t *testing.T, cs *mcp.ClientSession, name string) *mcp.Tool {
	t.Helper()
	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return nil
}

// assertReadOnlyAnnotations checks the annotation contract every read tool in
// this task shares: read-only, idempotent, and explicitly non-destructive
// (the SDK's DestructiveHint default is true, so it must be set).
func assertReadOnlyAnnotations(t *testing.T, cs *mcp.ClientSession, name string) {
	t.Helper()
	tool := toolNamed(t, cs, name)
	assert.NotContains(t, tool.Name, "melange", "tool names are unprefixed")
	assert.NotEmpty(t, tool.Description, "every tool documents its workflow role")
	require.NotNil(t, tool.Annotations)
	assert.True(t, tool.Annotations.ReadOnlyHint, "%s is a read tool", name)
	assert.True(t, tool.Annotations.IdempotentHint, "%s is idempotent", name)
	require.NotNil(t, tool.Annotations.DestructiveHint,
		"DestructiveHint must be set explicitly (SDK default is true)")
	assert.False(t, *tool.Annotations.DestructiveHint)
	assert.Nil(t, tool.OutputSchema, "no output schema until Task 5 (Out = any)")
}

func TestListReposPassesResponseBytesThroughAndDefaultsThePage(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos"),
		httpmock.JSONResponse(200, json.RawMessage(repoListBody)))

	cs, wire := connect(t, registryProvider(t, reg))
	// nil arguments marshal to a literal "arguments": null — the shape that
	// crashes the SDK's default-filling, so the page default lives in the
	// handler instead of the schema.
	res := callTool(t, cs, "list_repos", nil)

	assert.False(t, res.IsError)
	assert.Equal(t, repoListBody, textOf(t, res))

	query := reg.Requests[0].URL.Query()
	assert.Equal(t, "30", query.Get("limit"), "an omitted limit takes the schema default")
	assert.Equal(t, "0", query.Get("offset"))
	assert.False(t, query.Has("search"), "an omitted search is not sent as an empty filter")

	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+repoListBody,
		"the response bytes cross the wire verbatim")
	reg.Verify(t)
}

func TestListReposForwardsSearchAndPagination(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos"),
		httpmock.JSONResponse(200, json.RawMessage(repoListBody)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "list_repos", map[string]any{
		"search": "whisper", "limit": 100, "offset": 60,
	})
	assert.False(t, res.IsError)

	query := reg.Requests[0].URL.Query()
	assert.Equal(t, "whisper", query.Get("search"))
	assert.Equal(t, "100", query.Get("limit"))
	assert.Equal(t, "60", query.Get("offset"))
	reg.Verify(t)
}

func TestListReposRejectsOutOfRangePageBeforeCallingTheAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"above max", map[string]any{"limit": 101}},
		{"below min", map[string]any{"limit": 0}},
		{"negative offset", map[string]any{"offset": -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// An empty registry: any HTTP call would fail the stub lookup.
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "list_repos", tc.args)

			assert.True(t, res.IsError, "the input schema bounds the page before any request")
			assert.Empty(t, reg.Requests, "no API call is made for invalid arguments")
		})
	}
}

func TestGetRepoPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny"),
		httpmock.JSONResponse(200, json.RawMessage(repoBody)))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_repo", map[string]any{"repo": "zetic/whisper-tiny"})

	assert.False(t, res.IsError)
	assert.Equal(t, repoBody, textOf(t, res))

	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+repoBody)
	reg.Verify(t)
}

func TestGetRepoInvalidRepoArgumentIsToolErrorWithoutAnAPICall(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	res := callTool(t, cs, "get_repo", map[string]any{"repo": "whisper-tiny"})

	assert.True(t, res.IsError, "a malformed repo is a tool error, not a Go error")
	assert.Contains(t, textOf(t, res), "ACCOUNT/NAME")
	assert.Empty(t, reg.Requests, "a malformed repo never reaches the API")
}

func TestGetRepoNotFoundIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/nope"),
		httpmock.JSONResponse(http.StatusNotFound, json.RawMessage(
			`{"type":"error","error":{"type":"not_found_error","message":"repository not found"},"request_id":"req_9"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_repo", map[string]any{"repo": "zetic/nope"})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "repository not found")
	reg.Verify(t)
}

func TestRepoToolAnnotations(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	for _, name := range []string{"list_repos", "get_repo"} {
		t.Run(name, func(t *testing.T) { assertReadOnlyAnnotations(t, cs, name) })
	}
}
