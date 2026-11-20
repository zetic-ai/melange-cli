package mcp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// Keys are in non-alphabetical order and the values carry number text a
// float32 round trip would rewrite (0.463), so a re-marshal through typed
// structs breaks the byte assertions.
const (
	programPath = "/v1/programs/ACME-PROGRAM/benchmarks"
	programBody = `{"schema_version":"program-benchmark/v1","program":"ACME-PROGRAM",` +
		`"targets":[{"soc":"SOC-A","name":"Chip A","form_factor":"wearable","has_results":true,` +
		`"device":{"name":"Board A"},"notes":[]},` +
		`{"soc":"SOC-B","name":"Chip B","form_factor":"phone","has_results":false,` +
		`"device":{"name":"Phone B","model":"PB-1"},"notes":[]}],` +
		`"models":[{"slug":"speech","task":"tts","name":"Speech","order":40,"variants":[],` +
		`"results":[{"soc":"SOC-A","measured_on":"Board A","as_of":"2026-11-18",` +
		`"revision":1,"updated_at":"2026-11-18T00:00:00Z","software":{},"method":{},` +
		`"highlights":[],"notes":{"speed":[],"accuracy":[]},"tables":[],` +
		`"builds":[{"key":"ours","label":"Ours & Co <v1>","runtime":"llama_cpp",` +
		`"status":"measured","modes":["speed","accuracy"],"highlight":true,"baseline":false,"notes":[],` +
		`"speed":{"real_time_factor":0.463},"accuracy":{}}]}]}],` +
		`"metrics":{"tts":[]},"updated_at":"2026-11-18T00:00:00Z"}`
)

func TestGetProgramBenchmarksPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", programPath), jsonBody(200, programBody))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_program_benchmarks", map[string]any{"program": "ACME-PROGRAM"})

	assert.False(t, res.IsError, textOf(t, res))
	assert.Equal(t, programBody, textOf(t, res))

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, programBody)
	reg.Verify(t)
}

func TestGetProgramBenchmarksFiltersWithoutReencoding(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", programPath), jsonBody(200, programBody))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_program_benchmarks",
		map[string]any{"program": "ACME-PROGRAM", "model": "speech", "soc": "soc-a"})

	require.False(t, res.IsError, textOf(t, res))
	text := textOf(t, res)
	assert.Contains(t, text, `"real_time_factor":0.463`)
	assert.Contains(t, text, `"label":"Ours & Co <v1>"`, "no HTML escaping")
	assert.NotContains(t, text, "SOC-B")

	var page struct {
		Targets []json.RawMessage `json:"targets"`
		Models  []json.RawMessage `json:"models"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &page))
	assert.Len(t, page.Targets, 1)
	assert.Len(t, page.Models, 1)
}

func TestGetProgramBenchmarksUnknownSocIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", programPath), jsonBody(200, programBody))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_program_benchmarks", map[string]any{"program": "ACME-PROGRAM", "soc": "SOC-Z"})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "it lists SOC-A, SOC-B")
}

func TestGetProgramBenchmarksRequiresAProgram(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	res := callTool(t, cs, "get_program_benchmarks", map[string]any{"program": " "})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "access_programs")
	assert.Empty(t, reg.Requests)
}

func TestGetProgramBenchmarksNotFoundIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/programs/NOPE/benchmarks"),
		httpmock.JSONResponse(http.StatusNotFound, json.RawMessage(
			`{"type":"error","error":{"type":"not_found_error","message":"Program not found"},"request_id":"req_15"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_program_benchmarks", map[string]any{"program": "NOPE"})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "Program not found")
	reg.Verify(t)
}

func TestProgramToolAnnotations(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	assertReadOnlyAnnotations(t, cs, "get_program_benchmarks")
}
