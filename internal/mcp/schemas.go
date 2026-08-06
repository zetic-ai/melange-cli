package mcp

import (
	"embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// schemaFiles embeds the generated tool output schemas. They are derived from
// the vendored OpenAPI spec by `go run ./tools/mcpschemas` (part of
// `make gen`) and committed; gen-check guards them against drifting from the
// spec.
//
//go:embed schemas/*.json
var schemaFiles embed.FS

// outputSchemas memoizes each tool's parsed output schema (tool name ->
// *jsonschema.Schema) so the embedded JSON is unmarshaled once per process,
// not once per server. The stable pointer matters beyond the saved parse: the
// SDK's mcp.SchemaCache keys resolved schemas for explicitly provided schemas
// by POINTER identity (go-sdk v1.7.0 SchemaCache.getBySchema), so the HTTP
// transport's one-server-per-request design only gets cache hits if every New
// registers the very same schema pointer. Sharing is safe: nothing mutates a
// schema after it is returned, and jsonschema's Resolve documents that "the
// same schema may be resolved multiple times" — all resolution state lives in
// the Resolved value, not on the Schema.
var outputSchemas sync.Map

// outputSchema returns one tool's generated output schema. Every call for the
// same tool returns the same pointer (see outputSchemas).
//
// It panics when the embedded file is missing or unparseable: both mean the
// binary was built from a tree where `make gen` did not run (or the generator
// broke), which is a programming error at registration time — exactly like
// inputSchemaFor's panic — not a runtime condition any caller could handle.
// The SDK's AddTool then resolves the schema and panics itself if it is not
// valid JSON Schema, so New fails loudly either way.
func outputSchema(tool string) *jsonschema.Schema {
	if cached, ok := outputSchemas.Load(tool); ok {
		return cached.(*jsonschema.Schema)
	}
	data, err := schemaFiles.ReadFile("schemas/" + tool + ".json")
	if err != nil {
		panic(fmt.Sprintf("mcp: no embedded output schema for tool %q (run make gen): %v", tool, err))
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		panic(fmt.Sprintf("mcp: parsing output schema for tool %q: %v", tool, err))
	}
	// LoadOrStore converges concurrent first callers on one winner, so the
	// pointer handed out is stable even when two requests race the first parse.
	cached, _ := outputSchemas.LoadOrStore(tool, &s)
	return cached.(*jsonschema.Schema)
}
