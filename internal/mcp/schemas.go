package mcp

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// schemaFiles embeds the generated tool output schemas. They are derived from
// the vendored OpenAPI spec by `go run ./tools/mcpschemas` (part of
// `make gen`) and committed; gen-check guards them against drifting from the
// spec.
//
//go:embed schemas/*.json
var schemaFiles embed.FS

// outputSchema returns one tool's generated output schema.
//
// It panics when the embedded file is missing or unparseable: both mean the
// binary was built from a tree where `make gen` did not run (or the generator
// broke), which is a programming error at registration time — exactly like
// inputSchemaFor's panic — not a runtime condition any caller could handle.
// The SDK's AddTool then resolves the schema and panics itself if it is not
// valid JSON Schema, so New fails loudly either way.
func outputSchema(tool string) *jsonschema.Schema {
	data, err := schemaFiles.ReadFile("schemas/" + tool + ".json")
	if err != nil {
		panic(fmt.Sprintf("mcp: no embedded output schema for tool %q (run make gen): %v", tool, err))
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		panic(fmt.Sprintf("mcp: parsing output schema for tool %q: %v", tool, err))
	}
	return &s
}
