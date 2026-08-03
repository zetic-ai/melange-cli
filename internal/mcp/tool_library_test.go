package mcp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// Bodies use non-alphabetical key order so any re-marshal through a typed
// struct (which would sort keys) breaks the byte-equality assertions.
//
// The `<`, `>`, and `&` characters are equally deliberate: readmes and
// descriptions are prose and carry them routinely, and json.Marshal rewrites
// them as <, >, and & whenever it re-emits a json.RawMessage.
// An envelope built with HTML escaping fails on these bodies.
const (
	libraryListBody = `{"results":[{"full_name":"zetic/whisper-tiny","provider":{"name":"Zetic"},` +
		`"use_case":"speech","model_type":"onnx","description":"speech <-> text & subtitles"}],"count":1}`
	libraryProvidersBody = `{"results":[{"name":"Zetic & Partners <labs>","model_count":12}],"count":1}`
	libraryModelBody     = `{"full_name":"zetic/whisper-tiny","account":"zetic","name":"whisper-tiny",` +
		`"provider":{"name":"Zetic"},"use_case":"speech","model_type":"onnx",` +
		`"readme":"# whisper\n<img src=\"logo.png\"> speech & text"}`
)

func TestSearchLibraryPassesResponseBytesThroughAndDefaultsThePage(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/library/models"),
		jsonBody(200, libraryListBody))

	cs, wire := connect(t, registryProvider(t, reg))
	// nil arguments marshal to a literal "arguments": null — the shape that
	// crashes the SDK's default-filling, so the page default lives in the
	// handler instead of the schema.
	res := callTool(t, cs, "search_library", nil)

	assert.False(t, res.IsError)
	assert.Equal(t, libraryListBody, textOf(t, res))

	query := reg.Requests[0].URL.Query()
	assert.Equal(t, "30", query.Get("limit"), "an omitted limit takes the default page size")
	assert.Equal(t, "0", query.Get("offset"))
	assert.False(t, query.Has("search"), "an omitted filter is not sent as an empty one")
	assert.False(t, query.Has("provider"))
	assert.False(t, query.Has("task"))

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, libraryListBody)
	reg.Verify(t)
}

func TestSearchLibraryForwardsEveryFilter(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/library/models"),
		jsonBody(200, libraryListBody))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "search_library", map[string]any{
		"search": "whisper", "provider": "Zetic", "task": []string{"speech", "llm"},
		"limit": 5, "offset": 10,
	})
	assert.False(t, res.IsError)

	query := reg.Requests[0].URL.Query()
	assert.Equal(t, "whisper", query.Get("search"))
	assert.Equal(t, "Zetic", query.Get("provider"))
	assert.Equal(t, []string{"speech", "llm"}, query["task"],
		"task is repeatable: every value must reach the API, in order")
	assert.Equal(t, "5", query.Get("limit"))
	assert.Equal(t, "10", query.Get("offset"))
	reg.Verify(t)
}

func TestSearchLibraryWithoutIncludeProvidersSkipsTheProvidersCall(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/library/models"),
		jsonBody(200, libraryListBody))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "search_library", map[string]any{"include_providers": false})

	assert.False(t, res.IsError)
	require.Len(t, reg.Requests, 1, "providers are fetched only when asked for")
	reg.Verify(t)
}

func TestSearchLibraryIncludeProvidersEmitsCompositeEnvelope(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/library/models"),
		jsonBody(200, libraryListBody))
	reg.Register(httpmock.REST("GET", "/v1/library/providers"),
		jsonBody(200, libraryProvidersBody))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "search_library", map[string]any{"include_providers": true})

	assert.False(t, res.IsError)
	want := `{"models":` + libraryListBody + `,"providers":` + libraryProvidersBody + `}`
	assert.Equal(t, want, textOf(t, res),
		"the envelope names both halves and keeps each response's bytes intact, "+
			"including the < and & an escaping re-marshal would rewrite")

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, want)
	reg.Verify(t)
}

func TestSearchLibraryIncludeProvidersSurfacesTheProvidersFailure(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/library/models"),
		jsonBody(200, libraryListBody))
	reg.Register(httpmock.REST("GET", "/v1/library/providers"),
		httpmock.JSONResponse(http.StatusInternalServerError, json.RawMessage(
			`{"type":"error","error":{"type":"api_error","message":"provider index unavailable"},"request_id":"req_13"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "search_library", map[string]any{"include_providers": true})

	assert.True(t, res.IsError, "a half-built envelope is never returned as success")
	assert.Contains(t, textOf(t, res), "provider index unavailable")
	reg.Verify(t)
}

func TestSearchLibraryRejectsInvalidFiltersBeforeCallingTheAPI(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))

	// The vocabulary and bounds must reach the client, not just the handler: an
	// unadvertised task would travel to the API as a filter matching nothing,
	// which looks exactly like an empty library.
	schema, err := json.Marshal(toolNamed(t, cs, "search_library").InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"enum":["vision","llm","nlp","speech","other"]`,
		"the task vocabulary is advertised")
	assert.Contains(t, string(schema), `"maximum":100`, "the page size cap is advertised")
	assert.Contains(t, string(schema), `"minimum":1`, "a page of zero rows is refused")

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"unknown task", map[string]any{"task": []string{"audio"}}},
		{"one bad task among good ones", map[string]any{"task": []string{"speech", "audio"}}},
		{"page above max", map[string]any{"limit": 101}},
		{"negative offset", map[string]any{"offset": -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "search_library", tc.args)

			assert.True(t, res.IsError)
			// The SDK prefixes schema-validation failures this way; without it,
			// an IsError result could just as well be the unmatched-stub
			// transport error, which would pass with no constraints at all.
			assert.Contains(t, textOf(t, res), `validating "arguments"`,
				"the filter is rejected by the schema, not by a failed request")
			assert.Empty(t, reg.Requests)
		})
	}
}

func TestGetLibraryModelPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/library/models/zetic/whisper-tiny"),
		jsonBody(200, libraryModelBody))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_library_model", map[string]any{"library_model": "zetic/whisper-tiny"})

	assert.False(t, res.IsError)
	assert.Equal(t, libraryModelBody, textOf(t, res))

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, libraryModelBody)
	reg.Verify(t)
}

func TestGetLibraryModelMalformedCoordinatePointsAtTheLibrary(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	res := callTool(t, cs, "get_library_model", map[string]any{"library_model": "whisper-tiny"})

	assert.True(t, res.IsError, "a malformed coordinate is a tool error, not a Go error")
	text := textOf(t, res)
	assert.Contains(t, text, "ACCOUNT/NAME")
	assert.Contains(t, text, "search_library",
		"library models are discovered through the library, not through list_repos")
	assert.NotContains(t, text, "list_repos",
		"a public library entry need not appear in the caller's own repositories")
	assert.Empty(t, reg.Requests)
}

func TestGetLibraryModelNotFoundIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/library/models/zetic/nope"),
		httpmock.JSONResponse(http.StatusNotFound, json.RawMessage(
			`{"type":"error","error":{"type":"not_found_error","message":"library model not found"},"request_id":"req_14"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_library_model", map[string]any{"library_model": "zetic/nope"})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "library model not found")
	reg.Verify(t)
}

func TestLibraryToolAnnotations(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	for _, name := range []string{"search_library", "get_library_model"} {
		t.Run(name, func(t *testing.T) { assertReadOnlyAnnotations(t, cs, name) })
	}
}

func TestSearchLibraryDocumentsItsCompositeEnvelope(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	tool := toolNamed(t, cs, "search_library")

	// The description is the only place an agent learns the shape it gets back
	// when include_providers flips the result from a page to an envelope.
	assert.Contains(t, tool.Description, `{"models":`)
	assert.Contains(t, tool.Description, "include_providers")
}
