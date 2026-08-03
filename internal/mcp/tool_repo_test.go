package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// Bodies use non-alphabetical key order so any re-marshal through a typed
// struct (which would sort keys) breaks the byte-equality assertions. The `<`
// and `&` in the written bodies are equally deliberate: descriptions are prose
// and carry them, and json.Marshal rewrites them as &lt; and &amp; whenever it
// re-emits a json.RawMessage.
const (
	repoListBody = `{"results":[{"full_name":"zetic/whisper-tiny","account":"zetic","name":"whisper-tiny",` +
		`"is_private":false,"model_type":"general","tags":["speech"],` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}],"count":1}`
	repoBody = `{"name":"whisper-tiny","account":"zetic","full_name":"zetic/whisper-tiny",` +
		`"is_private":false,"model_type":"general","tags":["speech"],"description":"tiny",` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`
	writtenRepoBody = `{"full_name":"zetic/whisper-tiny","account":"zetic","name":"whisper-tiny",` +
		`"is_private":true,"model_type":"general","tags":[],` +
		`"description":"speech <-> text & subtitles",` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`
)

// requestBody returns the exact bytes a recorded request carried.
func requestBody(t *testing.T, req *http.Request) string {
	t.Helper()
	require.NotNil(t, req.GetBody, "request must expose a replayable body")
	rc, err := req.GetBody()
	require.NoError(t, err)
	defer rc.Close() //nolint:errcheck
	raw, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(raw)
}

// callTool runs one tool call over the in-memory transport. Every successful
// result is additionally validated against the tool's advertised output
// schema, so each passthrough test doubles as a schema-conformance test (the
// SDK itself never validates a manually set StructuredContent — see
// assertConformsToOutputSchema).
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err, "API and argument failures are tool errors, not protocol errors")
	assertConformsToOutputSchema(t, name, res)
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

// assertOpenWorldHint checks that a tool states its blast radius explicitly.
// The MCP default for OpenWorldHint is true, so a forgotten hint tells the
// agent a Melange-API-only tool may reach arbitrary third-party systems.
func assertOpenWorldHint(t *testing.T, tool *mcp.Tool, openWorld bool) {
	t.Helper()
	require.NotNil(t, tool.Annotations.OpenWorldHint,
		"OpenWorldHint must be set explicitly (MCP default is true)")
	assert.Equal(t, openWorld, *tool.Annotations.OpenWorldHint,
		"%s open-world hint", tool.Name)
}

// assertMutatingAnnotations checks a write tool's annotation contract: never
// read-only, and DestructiveHint/IdempotentHint/OpenWorldHint exactly as the
// catalog declares them. DestructiveHint and OpenWorldHint must be set
// explicitly — both default to true, so a forgotten hint would advertise
// create_repo as destructive and as reaching outside the Melange API.
func assertMutatingAnnotations(t *testing.T, cs *mcp.ClientSession, name string, destructive, idempotent, openWorld bool) {
	t.Helper()
	tool := toolNamed(t, cs, name)
	assert.NotContains(t, tool.Name, "melange", "tool names are unprefixed")
	assert.NotEmpty(t, tool.Description, "every tool documents its workflow role")
	require.NotNil(t, tool.Annotations)
	assert.False(t, tool.Annotations.ReadOnlyHint, "%s mutates state", name)
	assert.Equal(t, idempotent, tool.Annotations.IdempotentHint, "%s idempotency hint", name)
	require.NotNil(t, tool.Annotations.DestructiveHint,
		"DestructiveHint must be set explicitly (SDK default is true)")
	assert.Equal(t, destructive, *tool.Annotations.DestructiveHint, "%s destructive hint", name)
	assertOpenWorldHint(t, tool, openWorld)
	assert.NotNil(t, tool.OutputSchema, "%s advertises its OpenAPI-derived output schema", name)
}

// assertReadOnlyAnnotations checks the annotation contract every read tool in
// this task shares: read-only, idempotent, explicitly non-destructive, and
// explicitly closed-world — reads only ever touch the Melange API. Both
// DestructiveHint and OpenWorldHint default to true, so both must be set.
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
	assertOpenWorldHint(t, tool, false)
	assert.NotNil(t, tool.OutputSchema, "%s advertises its OpenAPI-derived output schema", name)
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
	assert.Equal(t, "30", query.Get("limit"), "an omitted limit takes the default page size")
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
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))

	// The bounds must reach the client, not just the handler. Asserting on the
	// advertised schema is what makes this test fail if withPageBounds is
	// dropped: an unbounded argument would otherwise reach the empty registry,
	// which reports a transport error that also surfaces as IsError.
	schema, err := json.Marshal(toolNamed(t, cs, "list_repos").InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"maximum":100`, "the page size cap is advertised")
	assert.Contains(t, string(schema), `"minimum":1`, "a page of zero rows is refused")

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
			// The SDK prefixes schema-validation failures this way; without it,
			// an IsError result could just as well be the unmatched-stub
			// transport error, which would pass even with no bounds at all.
			assert.Contains(t, textOf(t, res), `validating "arguments"`,
				"the argument is rejected by the schema, not by a failed request")
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

func TestCreateRepoSendsEveryProvidedFieldAndPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos"), jsonBody(http.StatusCreated, writtenRepoBody))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "create_repo", map[string]any{
		"name": "whisper-tiny", "private": true, "model_type": "llm",
		"use_case": "speech", "tags": []string{"asr", "tiny"}, "description": "d",
	})

	assert.False(t, res.IsError)
	assert.Equal(t, writtenRepoBody, textOf(t, res))
	require.Len(t, reg.Requests, 1)
	// The body carries exactly the caller's arguments under their request field
	// names, and nothing the caller did not ask for (readme) is invented.
	assert.JSONEq(t,
		`{"description":"d","is_private":true,"model_type":"llm","name":"whisper-tiny",`+
			`"tags":["asr","tiny"],"use_case":"speech"}`,
		requestBody(t, reg.Requests[0]))

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, writtenRepoBody)
	reg.Verify(t)
}

func TestCreateRepoOmittedOptionsStayOutOfTheBody(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos"), jsonBody(http.StatusCreated, writtenRepoBody))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "create_repo", map[string]any{"name": "whisper-tiny"})

	assert.False(t, res.IsError)
	require.Len(t, reg.Requests, 1)
	// The general default is applied by the handler (a schema default would
	// crash the SDK on "arguments": null), and an omitted option is absent
	// rather than sent as an empty value the API would have to interpret.
	assert.JSONEq(t, `{"model_type":"general","name":"whisper-tiny"}`, requestBody(t, reg.Requests[0]))
	reg.Verify(t)
}

func TestCreateRepoRejectsAnAccountPrefixedNameWithoutAnAPICall(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	res := callTool(t, cs, "create_repo", map[string]any{"name": "zetic/whisper-tiny"})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "without an ACCOUNT/ prefix",
		"the agent is told how to fix the name, not just that it failed")
	assert.Empty(t, reg.Requests, "a repository is never created under a guessed name")
}

func TestCreateRepoRejectsUnknownVocabularyBeforeCallingTheAPI(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))

	// The vocabulary must reach the client, not just the handler: an
	// unadvertised value would otherwise be forwarded and rejected as a 422
	// only after the agent has already committed to the call.
	schema, err := json.Marshal(toolNamed(t, cs, "create_repo").InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"enum":["general","llm"]`)
	assert.Contains(t, string(schema), `"enum":["vision","nlp","llm","speech","other"]`)

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"unknown model type", map[string]any{"name": "x", "model_type": "onnx"}},
		{"unknown use case", map[string]any{"name": "x", "use_case": "audio"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "create_repo", tc.args)

			assert.True(t, res.IsError)
			// The SDK prefixes schema-validation failures this way; without it,
			// an IsError result could just as well be the unmatched-stub
			// transport error, which would pass with no enum at all.
			assert.Contains(t, textOf(t, res), `validating "arguments"`,
				"the value is rejected by the schema, not by a failed request")
			assert.Empty(t, reg.Requests)
		})
	}
}

func TestUpdateRepoPatchBodyCarriesOnlyTheProvidedFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{
			// "" is the documented way to clear a description, so it must be
			// sent rather than treated as an omitted argument.
			name: "empty description clears it",
			args: map[string]any{"repo": "zetic/whisper-tiny", "description": ""},
			want: `{"description":""}`,
		},
		{
			name: "private false publishes",
			args: map[string]any{"repo": "zetic/whisper-tiny", "private": false},
			want: `{"is_private":false}`,
		},
		{
			// An empty list is a real edit (drop every tag), not an omission.
			name: "empty tags clear the set",
			args: map[string]any{"repo": "zetic/whisper-tiny", "tags": []string{}},
			want: `{"tags":[]}`,
		},
		{
			name: "every field",
			args: map[string]any{
				"repo": "zetic/whisper-tiny", "description": "d", "private": true,
				"use_case": "speech", "tags": []string{"asr"},
			},
			want: `{"description":"d","is_private":true,"tags":["asr"],"use_case":"speech"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"),
				jsonBody(200, writtenRepoBody))

			cs, _ := connect(t, registryProvider(t, reg))
			res := callTool(t, cs, "update_repo", tc.args)

			assert.False(t, res.IsError)
			require.Len(t, reg.Requests, 1)
			assert.JSONEq(t, tc.want, requestBody(t, reg.Requests[0]),
				"an untouched field must stay out of the PATCH body entirely")
			reg.Verify(t)
		})
	}
}

func TestUpdateRepoPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"),
		jsonBody(200, writtenRepoBody))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "update_repo", map[string]any{
		"repo": "zetic/whisper-tiny", "description": "d",
	})

	assert.False(t, res.IsError)
	assert.Equal(t, writtenRepoBody, textOf(t, res),
		"the updated repository's bytes survive, including the < and & an escaping re-marshal would rewrite")

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, writtenRepoBody)
	reg.Verify(t)
}

func TestUpdateRepoWithNothingToChangeIsRefusedWithoutAnAPICall(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"repo alone", map[string]any{"repo": "zetic/whisper-tiny"}},
		{
			// The schema admits null for the pointer-valued fields. Null must
			// read as "leave it alone" — reading it as "clear it" would delete
			// a description the caller never asked to touch.
			name: "explicit nulls",
			args: map[string]any{"repo": "zetic/whisper-tiny", "description": nil, "private": nil, "tags": nil},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "update_repo", tc.args)

			assert.True(t, res.IsError)
			assert.Contains(t, textOf(t, res), "nothing to update",
				"an empty PATCH is refused with guidance, not sent as a no-op write")
			assert.Empty(t, reg.Requests)
		})
	}
}

func TestDeleteRepoWithoutAMatchingConfirmationNeverCallsTheAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"confirm absent", map[string]any{"repo": "zetic/whisper-tiny"}},
		{"confirm empty", map[string]any{"repo": "zetic/whisper-tiny", "confirm": ""}},
		{"different repo", map[string]any{"repo": "zetic/whisper-tiny", "confirm": "zetic/other"}},
		{"name only", map[string]any{"repo": "zetic/whisper-tiny", "confirm": "whisper-tiny"}},
		{"case differs", map[string]any{"repo": "zetic/whisper-tiny", "confirm": "zetic/Whisper-Tiny"}},
		{"trailing space", map[string]any{"repo": "zetic/whisper-tiny", "confirm": "zetic/whisper-tiny "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "delete_repo", tc.args)

			assert.True(t, res.IsError)
			text := textOf(t, res)
			// Discriminating on the refusal's own words: an IsError result with
			// an empty registry could equally be a schema failure or an
			// unmatched stub, neither of which proves the gate ran.
			assert.Contains(t, text, "Nothing was deleted")
			assert.Contains(t, text, "explicit consent from the user",
				"the agent is told to get consent, not just to retry with confirm")
			assert.Contains(t, text, `confirm: "zetic/whisper-tiny"`,
				"the refusal spells out the exact value to send")
			assert.Empty(t, reg.Requests, "an unconfirmed deletion never reaches the API")
		})
	}
}

func TestDeleteRepoWithAMatchingConfirmationDeletes(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("DELETE", "/v1/repos/zetic/whisper-tiny"),
		httpmock.StatusStringResponse(http.StatusNoContent, ""))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "delete_repo", map[string]any{
		"repo": "zetic/whisper-tiny", "confirm": "zetic/whisper-tiny",
	})

	assert.False(t, res.IsError)
	// The API answers 204 with no body; the tool still has to say what happened.
	want := `{"deleted":true,"repo":"zetic/whisper-tiny"}`
	assert.Equal(t, want, textOf(t, res))
	require.Len(t, reg.Requests, 1)
	assert.Equal(t, http.MethodDelete, reg.Requests[0].Method)

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, want)
	reg.Verify(t)
}

func TestDeleteRepoAPIFailureIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("DELETE", "/v1/repos/zetic/whisper-tiny"),
		httpmock.JSONResponse(http.StatusForbidden, json.RawMessage(
			`{"type":"error","error":{"type":"permission_error","message":"only the owner can delete"},"request_id":"req_21"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "delete_repo", map[string]any{
		"repo": "zetic/whisper-tiny", "confirm": "zetic/whisper-tiny",
	})

	assert.True(t, res.IsError, "a refused deletion is never reported as deleted")
	text := textOf(t, res)
	assert.Contains(t, text, "only the owner can delete")
	assert.Contains(t, text, "melange auth status")
	reg.Verify(t)
}

func TestRepoWriteToolAnnotations(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	for _, tc := range []struct {
		name                    string
		destructive, idempotent bool
	}{
		{"create_repo", false, false},
		{"update_repo", false, true},
		{"delete_repo", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Every repo write goes to the Melange API only: closed-world.
			assertMutatingAnnotations(t, cs, tc.name, tc.destructive, tc.idempotent, false)
		})
	}
}

func TestDeleteRepoAdvertisesItsConsentContract(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	tool := toolNamed(t, cs, "delete_repo")

	// The description is where an agent learns the gate exists before it ever
	// calls the tool, and that consent — not just the argument — is required.
	assert.Contains(t, tool.Description, "cannot be undone")
	assert.Contains(t, tool.Description, "explicit consent")

	schema, err := json.Marshal(tool.InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"required":["repo"]`,
		"confirm stays optional in the schema so the handler's consent guidance is what an agent sees")
}
