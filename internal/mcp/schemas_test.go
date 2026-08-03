package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/fixturetest"
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
	// The EnableLocalTools catalog is the superset, so the local-only
	// upload_model is held to the same schema discipline.
	cs := connectWith(t, "test", Options{EnableLocalTools: true})
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

// fixtureBody reads one contract fixture's response body.
func fixtureBody(t *testing.T, name string) json.RawMessage {
	t.Helper()
	fx := fixturetest.Load(t, strings.TrimSuffix(name, ".json"))
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

	// wraps records, for each fixture FixtureTool maps, how its body appears
	// inside the mapped tool's structured content; the tool itself comes from
	// the shared FixtureTool table.
	wraps := map[string]func(t *testing.T, body json.RawMessage) any{
		"get_me":            passthrough,
		"get_usage":         wrap("usage"),
		"get_usage_quotas":  wrap("quotas"),
		"get_billing_plan":  wrap("plan"),
		"list_repos":        passthrough,
		"create_repo":       passthrough,
		"get_repo":          passthrough,
		"get_model":         passthrough,
		"get_model_status":  passthrough,
		"set_default_model": passthrough,
		"import_model":      passthrough,
		// The targets listing only ever reaches a caller inside the
		// include_targets envelope, next to the model it belongs to.
		"list_model_targets": func(t *testing.T, body json.RawMessage) any {
			t.Helper()
			return map[string]any{
				"model":   unmarshalAny(t, fixtureBody(t, "get_model.json")),
				"targets": unmarshalAny(t, body),
			}
		},
		"get_deployment_options": passthrough,
		"get_deployment_guide":   passthrough,
		"get_general_report":     passthrough,
		"get_llm_report":         passthrough,
		"get_package_report":     passthrough,
		"list_library_models":    passthrough,
		// The provider list only ever reaches a caller inside the
		// include_providers envelope, next to a model page.
		"list_library_providers": func(t *testing.T, body json.RawMessage) any {
			t.Helper()
			return map[string]any{
				"models":    unmarshalAny(t, fixtureBody(t, "list_library_models.json")),
				"providers": unmarshalAny(t, body),
			}
		},
		"get_library_model":             passthrough,
		"create_download_authorization": passthrough,
		// The upload-complete response reaches a caller inside upload_model's
		// envelope, with its model reference repeated alongside.
		"complete_model_upload": func(t *testing.T, body json.RawMessage) any {
			t.Helper()
			session, ok := unmarshalAny(t, body).(map[string]any)
			require.True(t, ok)
			require.NotNil(t, session["model"], "the fixture completion carries a model reference")
			return map[string]any{"session": session, "model": session["model"]}
		},
	}
	for stem := range wraps {
		_, mapped := FixtureTool[stem]
		assert.True(t, mapped, "wrap entry %s matches no FixtureTool fixture", stem)
	}

	// consumedInternally lists mapped fixtures whose response bodies the tool
	// consumes without ever emitting them as structuredContent, so there is no
	// output shape to hold against the tool's schema. Both carry signed upload
	// URLs — short-lived credentials that must never reach a transcript — so
	// their absence from tool output is deliberate, not an oversight; the
	// fixture round-trips in fixtures_test.go still exercise both exchanges
	// through upload_model.
	consumedInternally := map[string]string{
		"create_model_upload": "session-create response (signed upload URLs) drives the transfer, never the output",
		"get_model_upload":    "session detail (reissued upload URLs on resume) drives state rebuild, never the output",
	}
	for stem := range consumedInternally {
		_, mapped := FixtureTool[stem]
		assert.True(t, mapped, "consumedInternally entry %s matches no FixtureTool fixture", stem)
		_, wrapped := wraps[stem]
		assert.False(t, wrapped, "fixture %s is both wrapped and consumed internally", stem)
	}

	entries, err := os.ReadDir(fixturetest.Dir(t))
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		stem := strings.TrimSuffix(name, ".json")
		tool, mapped := FixtureTool[stem]
		if !mapped {
			// Every fixture must be mapped or skipped, so a new backend
			// fixture forces a deliberate decision.
			_, isSkipped := FixtureSkipped[stem]
			assert.True(t, isSkipped,
				"fixture %s is neither in FixtureTool nor in FixtureSkipped (internal/mcp/fixtures.go)", name)
			continue
		}
		_, alsoSkipped := FixtureSkipped[stem]
		assert.False(t, alsoSkipped, "fixture %s is both mapped and skipped", name)
		if _, internal := consumedInternally[stem]; internal {
			continue
		}
		wrapFn, ok := wraps[stem]
		require.True(t, ok, "fixture %s is mapped to %s but has no wrap entry here", name, tool)
		t.Run(name, func(t *testing.T) {
			validateAgainst(t, tool, wrapFn(t, fixtureBody(t, name)))
		})
	}
	for stem := range FixtureTool {
		_, err := os.Stat(filepath.Join(fixturetest.Dir(t), stem+".json"))
		assert.NoError(t, err, "mapped fixture %s no longer exists", stem)
	}
	for stem := range FixtureSkipped {
		_, err := os.Stat(filepath.Join(fixturetest.Dir(t), stem+".json"))
		assert.NoError(t, err, "skipped fixture %s no longer exists", stem)
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

	t.Run("upload envelope with a wait status", func(t *testing.T) {
		// A wait_seconds upload emits all three keys; no single fixture
		// exercises the composite.
		session, ok := unmarshalAny(t, fixtureBody(t, "complete_model_upload.json")).(map[string]any)
		require.True(t, ok)
		validateAgainst(t, "upload_model", map[string]any{
			"session": session,
			"model":   session["model"],
			"status":  unmarshalAny(t, fixtureBody(t, "get_model_status.json")),
		})
	})

	t.Run("upload envelope before a model exists", func(t *testing.T) {
		// A completion still VERIFYING has no model reference yet: the
		// envelope is the session alone.
		session := mutated(t, fixtureBody(t, "complete_model_upload.json"), "model", nil)
		validateAgainst(t, "upload_model", map[string]any{"session": session})
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
		{
			name:     "upload envelope without its required session half",
			tool:     "upload_model",
			instance: map[string]any{"model": map[string]any{"key": "m_1", "version": 1}},
		},
		{
			name: "upload envelope session with a state outside the enum",
			tool: "upload_model",
			instance: map[string]any{
				"session": mutated(t, fixtureBody(t, "complete_model_upload.json"), "state", "PAUSED"),
			},
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
