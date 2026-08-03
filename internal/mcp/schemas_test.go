package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// outputSchemaExceptions lists tools that deliberately ship WITHOUT an output
// schema, with the reason. It is empty: every tool's response was faithfully
// representable (see the mapping table in tools/mcpschemas/main.go). A tool
// added without a schema must either gain one in the generator catalog or be
// documented here.
var outputSchemaExceptions = map[string]string{}

// resolvedSchemas caches per-tool resolved output schemas for the conformance
// helpers; resolution is pure, so sharing across tests is safe.
var resolvedSchemas sync.Map

// resolvedOutputSchema resolves one tool's embedded output schema the same way
// mcp.AddTool does at registration.
func resolvedOutputSchema(t *testing.T, tool string) *jsonschema.Resolved {
	t.Helper()
	if cached, ok := resolvedSchemas.Load(tool); ok {
		return cached.(*jsonschema.Resolved)
	}
	resolved, err := outputSchema(tool).Resolve(nil)
	require.NoError(t, err, "output schema for %s must resolve", tool)
	resolvedSchemas.Store(tool, resolved)
	return resolved
}

// assertConformsToOutputSchema validates a successful tool result's
// structuredContent against the tool's advertised output schema.
//
// The SDK does NOT do this itself for these tools: go-sdk v1.7.0 only
// validates the typed Out value a handler returns, and every melange handler
// returns Out = nil while setting StructuredContent directly (byte-exact
// passthrough). This explicit step is what makes every passthrough test in
// the package double as a schema-conformance test.
func assertConformsToOutputSchema(t *testing.T, tool string, res *mcp.CallToolResult) {
	t.Helper()
	if res.IsError || res.StructuredContent == nil {
		return
	}
	if _, exempt := outputSchemaExceptions[tool]; exempt {
		return
	}
	assert.NoError(t, resolvedOutputSchema(t, tool).Validate(res.StructuredContent),
		"%s result must conform to its advertised output schema", tool)
}

func TestEveryEmbeddedSchemaParsesAndResolves(t *testing.T) {
	entries, err := schemaFiles.ReadDir("schemas")
	require.NoError(t, err)
	require.NotEmpty(t, entries, "the generated schemas must be embedded")

	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			data, err := schemaFiles.ReadFile("schemas/" + entry.Name())
			require.NoError(t, err)
			var s jsonschema.Schema
			require.NoError(t, json.Unmarshal(data, &s), "embedded schema must parse")
			_, err = s.Resolve(nil)
			require.NoError(t, err, "embedded schema must compile (AddTool would panic)")
		})
	}
}

func TestEveryRegisteredToolHasAnOutputSchemaOrDocumentedException(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	tools, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, tools.Tools)

	registered := make(map[string]bool, len(tools.Tools))
	for _, tool := range tools.Tools {
		registered[tool.Name] = true
		if _, exempt := outputSchemaExceptions[tool.Name]; exempt {
			assert.Nil(t, tool.OutputSchema,
				"%s is on the exception list yet advertises a schema; remove the stale entry", tool.Name)
			continue
		}
		assert.NotNil(t, tool.OutputSchema,
			"%s has no output schema: add it to the generator catalog or document the exception", tool.Name)
	}

	// The reverse direction: every embedded schema belongs to a registered
	// tool, so a renamed tool cannot leave a stale schema file behind.
	entries, err := schemaFiles.ReadDir("schemas")
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()[:len(entry.Name())-len(".json")]
		assert.True(t, registered[name],
			"embedded schema %s matches no registered tool (stale file? run make gen)", entry.Name())
	}
}

// fixturesDir locates the shared contract fixtures relative to this file.
func fixturesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(file), "..", "..", "openapi", "fixtures")
}

// fixtureBody reads one contract fixture's response body.
func fixtureBody(t *testing.T, name string) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixturesDir(t), name))
	require.NoError(t, err)
	var fx struct {
		Response struct {
			Body json.RawMessage `json:"body"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(data, &fx))
	require.NotEmpty(t, fx.Response.Body, "%s has no response body", name)
	return fx.Response.Body
}

// validateAgainst asserts an instance built from fixture JSON conforms to one
// tool's output schema.
func validateAgainst(t *testing.T, tool string, instance any) {
	t.Helper()
	assert.NoError(t, resolvedOutputSchema(t, tool).Validate(instance),
		"payload must conform to the %s output schema", tool)
}

// unmarshalAny decodes raw JSON the way the SDK hands structuredContent to a
// client: into untyped Go values.
func unmarshalAny(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

// TestContractFixturesConformToToolOutputSchemas validates every backend
// contract fixture that flows through an MCP tool against that tool's output
// schema — the fixtures are real TestClient responses, so this pins the
// generated schemas to what the backend actually sends, not just to the spec
// they were derived from.
func TestContractFixturesConformToToolOutputSchemas(t *testing.T) {
	// section wraps a per-operation body the way the composite handler does.
	wrap := func(key string) func(t *testing.T, body json.RawMessage) any {
		return func(t *testing.T, body json.RawMessage) any {
			t.Helper()
			return map[string]any{key: unmarshalAny(t, body)}
		}
	}
	passthrough := func(t *testing.T, body json.RawMessage) any {
		t.Helper()
		return unmarshalAny(t, body)
	}

	cases := map[string]struct {
		tool string
		wrap func(t *testing.T, body json.RawMessage) any
	}{
		"get_me.json":            {tool: "whoami", wrap: passthrough},
		"get_usage.json":         {tool: "get_account_info", wrap: wrap("usage")},
		"get_usage_quotas.json":  {tool: "get_account_info", wrap: wrap("quotas")},
		"get_billing_plan.json":  {tool: "get_account_info", wrap: wrap("plan")},
		"list_repos.json":        {tool: "list_repos", wrap: passthrough},
		"create_repo.json":       {tool: "create_repo", wrap: passthrough},
		"get_repo.json":          {tool: "get_repo", wrap: passthrough},
		"get_model.json":         {tool: "get_model", wrap: passthrough},
		"get_model_status.json":  {tool: "get_conversion_status", wrap: passthrough},
		"set_default_model.json": {tool: "set_default_model", wrap: passthrough},
		"import_model.json":      {tool: "import_model", wrap: passthrough},
		// The targets listing only ever reaches a caller inside the
		// include_targets envelope, next to the model it belongs to.
		"list_model_targets.json": {tool: "get_model", wrap: func(t *testing.T, body json.RawMessage) any {
			t.Helper()
			return map[string]any{
				"model":   unmarshalAny(t, fixtureBody(t, "get_model.json")),
				"targets": unmarshalAny(t, body),
			}
		}},
		"get_deployment_options.json": {tool: "get_deployment_info", wrap: passthrough},
		"get_deployment_guide.json":   {tool: "get_deployment_info", wrap: passthrough},
		"get_general_report.json":     {tool: "get_model_report", wrap: passthrough},
		"get_llm_report.json":         {tool: "get_model_report", wrap: passthrough},
		"get_package_report.json":     {tool: "get_model_report", wrap: passthrough},
		"list_library_models.json":    {tool: "search_library", wrap: passthrough},
		// The provider list only ever reaches a caller inside the
		// include_providers envelope, next to a model page.
		"list_library_providers.json": {tool: "search_library", wrap: func(t *testing.T, body json.RawMessage) any {
			t.Helper()
			return map[string]any{
				"models":    unmarshalAny(t, fixtureBody(t, "list_library_models.json")),
				"providers": unmarshalAny(t, body),
			}
		}},
		"get_library_model.json":             {tool: "get_library_model", wrap: passthrough},
		"create_download_authorization.json": {tool: "request_model_download", wrap: passthrough},
	}

	// Fixtures that flow through no MCP tool. Every fixture must be mapped or
	// listed here, so a new backend fixture forces a deliberate decision.
	skipped := map[string]string{
		"create_model_upload.json":          "model upload tools arrive with the upload PR",
		"create_model_upload_conflict.json": "409 error exchange; also an upload fixture",
		"get_model_upload.json":             "model upload tools arrive with the upload PR",
		"complete_model_upload.json":        "model upload tools arrive with the upload PR",
		"cancel_model_upload.json":          "model upload tools arrive with the upload PR",
		"error_401.json":                    "error envelope: failures surface as IsError text, not structuredContent",
		"error_422.json":                    "error envelope: failures surface as IsError text, not structuredContent",
		"error_422_enum.json":               "error envelope: failures surface as IsError text, not structuredContent",
	}

	entries, err := os.ReadDir(fixturesDir(t))
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		tc, mapped := cases[name]
		if !mapped {
			_, isSkipped := skipped[name]
			assert.True(t, isSkipped,
				"fixture %s is neither mapped to a tool nor on the documented skip list", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			validateAgainst(t, tc.tool, tc.wrap(t, fixtureBody(t, name)))
		})
	}
	for name := range cases {
		_, err := os.Stat(filepath.Join(fixturesDir(t), name))
		assert.NoError(t, err, "mapped fixture %s no longer exists", name)
	}
}

// TestSynthesizedAndCompositePayloadsConformToSchemas covers the payload
// shapes no single fixture exercises: the hand-authored delete_repo
// acknowledgement, the account envelope with every section, and the redacted
// download authorization.
func TestSynthesizedAndCompositePayloadsConformToSchemas(t *testing.T) {
	t.Run("delete_repo acknowledgement", func(t *testing.T) {
		validateAgainst(t, "delete_repo",
			map[string]any{"deleted": true, "repo": "zetic/whisper-tiny"})
	})

	t.Run("full account envelope", func(t *testing.T) {
		validateAgainst(t, "get_account_info", map[string]any{
			"usage":  unmarshalAny(t, fixtureBody(t, "get_usage.json")),
			"quotas": unmarshalAny(t, fixtureBody(t, "get_usage_quotas.json")),
			"plan":   unmarshalAny(t, fixtureBody(t, "get_billing_plan.json")),
		})
	})

	t.Run("redacted download authorization", func(t *testing.T) {
		// The default (include_urls omitted) output replaces every artifact
		// url with the redaction placeholder; the schema must accept it.
		body := unmarshalAny(t, fixtureBody(t, "create_download_authorization.json"))
		auth, ok := body.(map[string]any)
		require.True(t, ok)
		artifacts, ok := auth["artifacts"].([]any)
		require.True(t, ok, "the fixture carries artifacts to redact")
		for _, a := range artifacts {
			a.(map[string]any)["url"] = redactedURL
		}
		validateAgainst(t, "request_model_download", body)
	})
}

// TestOutputSchemasRejectForeignShapes proves the conformance harness has
// teeth: a payload that names the wrong envelope key, drops a required field,
// or contradicts the synthesized constant must fail validation.
func TestOutputSchemasRejectForeignShapes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tool     string
		instance any
	}{
		{
			name: "unknown account section key",
			tool: "get_account_info",
			instance: map[string]any{
				"billing": unmarshalAny(t, fixtureBody(t, "get_billing_plan.json")),
			},
		},
		{
			name:     "model missing its required fields",
			tool:     "get_model",
			instance: map[string]any{"key": "m_1"},
		},
		{
			name:     "delete acknowledgement claiming deleted false",
			tool:     "delete_repo",
			instance: map[string]any{"deleted": false, "repo": "zetic/whisper-tiny"},
		},
		{
			name:     "conversion status with a stage outside the enum",
			tool:     "get_conversion_status",
			instance: mutated(t, fixtureBody(t, "get_model_status.json"), "stage", "packaging"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Error(t, resolvedOutputSchema(t, tc.tool).Validate(tc.instance),
				"the %s schema must reject this shape", tc.tool)
		})
	}
}

// mutated decodes a fixture body and overwrites one top-level key.
func mutated(t *testing.T, raw json.RawMessage, key string, value any) any {
	t.Helper()
	body, ok := unmarshalAny(t, raw).(map[string]any)
	require.True(t, ok)
	body[key] = value
	return body
}
