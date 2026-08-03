package mcp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// reportBody renders a report response; the kind marker proves which endpoint
// answered. Keys are in non-alphabetical order so any re-marshal through a
// typed struct (which would sort them) breaks the byte-equality assertions.
func reportBody(kind string) string {
	return `{"report_type":"` + kind + `","model":{"key":"whisper-tiny-1","version":1},` +
		`"records":[{"device":{"marketing_name":"Pixel 8"},"metric":"latency_ms","value":12.5}]}`
}

const reportPathPrefix = "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1/reports/"

func TestGetModelReportDispatchesEachTypeToItsOwnEndpoint(t *testing.T) {
	// One case per report type: a handler that ignored report_type and always
	// called one endpoint would fail every other case's stub lookup.
	for _, reportType := range []string{"general", "llm", "package"} {
		t.Run(reportType, func(t *testing.T) {
			body := reportBody(reportType)
			reg := &httpmock.Registry{}
			reg.Register(httpmock.REST("GET", reportPathPrefix+reportType),
				httpmock.JSONResponse(200, json.RawMessage(body)))

			cs, wire := connect(t, registryProvider(t, reg))
			res := callTool(t, cs, "get_model_report", map[string]any{
				"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
				"report_type": reportType,
			})

			assert.False(t, res.IsError)
			assert.Equal(t, body, textOf(t, res))
			require.Len(t, reg.Requests, 1, "a named report type is never probed for")

			require.NoError(t, cs.Close())
			assert.Contains(t, wire.String(), `"structuredContent":`+body,
				"the response bytes cross the wire verbatim")
			reg.Verify(t)
		})
	}
}

func TestGetModelReportRejectsAnUnknownTypeBeforeCallingTheAPI(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))

	// The vocabulary must reach the client, not just the handler: without the
	// advertised enum, an unknown type would be caught only by the handler's
	// fallback, after the model had already spent a call guessing.
	schema, err := json.Marshal(toolNamed(t, cs, "get_model_report").InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"enum":["general","llm","package"]`,
		"the report types are advertised")

	for _, reportType := range []string{"benchmark", "General", ""} {
		t.Run("type "+reportType, func(t *testing.T) {
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "get_model_report", map[string]any{
				"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
				"report_type": reportType,
			})

			assert.True(t, res.IsError)
			// The SDK prefixes schema-validation failures this way; without it,
			// an IsError result could just as well be the unmatched-stub
			// transport error, which would pass with no enum at all.
			assert.Contains(t, textOf(t, res), `validating "arguments"`,
				"the type is rejected by the schema, not by a failed request")
			assert.Empty(t, reg.Requests, "no endpoint is guessed for an unknown report type")
		})
	}
}

func TestGetModelReportRequiresAReportType(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	res := callTool(t, cs, "get_model_report", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
	})

	assert.True(t, res.IsError, "report_type is required: the tool never probes")
	assert.Contains(t, textOf(t, res), `validating "arguments"`)
	assert.Empty(t, reg.Requests)
}

func TestGetModelReportInvalidRepoIsToolErrorWithoutAnAPICall(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	res := callTool(t, cs, "get_model_report", map[string]any{
		"repo": "whisper-tiny", "model_key": "whisper-tiny-1", "report_type": "general",
	})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "ACCOUNT/NAME")
	assert.Empty(t, reg.Requests)
}

func TestGetModelReportMissingReportIsToolError(t *testing.T) {
	// A model type that produces no llm report answers 404. The agent must see
	// that as an actionable failure, not as an empty report.
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", reportPathPrefix+"llm"),
		httpmock.JSONResponse(http.StatusNotFound, json.RawMessage(
			`{"type":"error","error":{"type":"not_found_error","message":"no llm report for this model"},"request_id":"req_12"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_model_report", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "report_type": "llm",
	})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "no llm report for this model")
	reg.Verify(t)
}

func TestGetModelReportAnnotationsAndDescription(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	assertReadOnlyAnnotations(t, cs, "get_model_report")

	tool := toolNamed(t, cs, "get_model_report")
	// The description is where an agent learns which type to ask for, and that
	// a not-found answer means "wrong shape", not "no data".
	for _, want := range []string{"general", "llm", "package", "not-found"} {
		assert.Contains(t, tool.Description, want)
	}
}
