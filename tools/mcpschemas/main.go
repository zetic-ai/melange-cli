// Command mcpschemas derives the MCP tool output schemas from the vendored
// OpenAPI 3.0 document (openapi/public-v1.3.0.json) and writes one
// self-contained JSON Schema draft 2020-12 file per tool to
// internal/mcp/schemas/<tool>.json.
//
// For each mapped operation it resolves the 2xx response schema, inlines every
// $ref (the components graph is acyclic — inlining is checked by the resolver
// erroring on unbounded depth), and undoes the OpenAPI 3.0 down-conversion:
// `nullable: true` becomes the draft 2020-12 union the source 3.1 spec had
// (a type array with "null", or anyOf for $ref wrappers). `xml`, `example`,
// `examples`, `discriminator`, and `externalDocs` are stripped defensively;
// none occur in the current spec. `default` annotations are kept: none occur
// in any mapped response schema today, and if one appears it is a faithful
// annotation the SDK only reads when filling typed outputs, which these
// passthrough tools never use.
//
// Every generated document is round-tripped through the SDK's jsonschema
// package and resolved before it is written, so `make gen` fails on a schema
// the server would panic on at registration.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/zetic-ai/melange-cli/internal/mcp"
)

func main() {
	input := flag.String("input", "openapi/public-v1.3.0.json", "OpenAPI 3.0 JSON document")
	output := flag.String("output", "internal/mcp/schemas", "directory for the generated per-tool schemas")
	flag.Parse()

	data, err := os.ReadFile(*input)
	if err != nil {
		fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		fatal(fmt.Errorf("decode %s: %w", *input, err))
	}
	g, err := newGenerator(doc)
	if err != nil {
		fatal(err)
	}
	if err := g.writeAll(*output); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %d tool output schemas to %s\n", len(catalog), *output)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "mcpschemas:", err)
	os.Exit(1)
}

// draft2020 is the dialect every generated schema declares.
const draft2020 = "https://json-schema.org/draft/2020-12/schema"

// refPrefix is the only $ref target namespace the resolver handles.
const refPrefix = "#/components/schemas/"

// tool is one catalog entry: an MCP tool name and how its output schema is
// composed from the spec's operations.
type tool struct {
	name  string
	build func(g *generator) (map[string]any, error)
}

// catalog is the mapping table: every tool registered in internal/mcp must
// appear here — the internal/mcp tests cross-check the registered catalog
// against the embedded schema files. A tool whose response could not be
// faithfully represented would be recorded here with a nil build and a
// comment; currently every tool has a faithful schema.
//
//   - Single-operation tools map to that operation's 2xx response schema.
//   - Mode-switching tools (get_model_report, get_deployment_info) advertise
//     the anyOf of the shapes their arguments select between.
//   - Composite envelope tools (get_model, search_library, get_account_info)
//     wrap the per-operation schemas in the envelope object the handler builds.
//   - delete_repo is hand-authored: the API answers 204 with no body, and the
//     handler synthesizes {"deleted":true,"repo":"…"} so the caller has
//     something to act on. There is no spec schema to derive it from.
//   - request_model_download derives from create_download_authorization; the
//     handler's default redaction replaces artifacts[].url with "<redacted>",
//     which still satisfies the spec's plain string type.
//   - upload_model's envelope is hand-authored per the delete_repo precedent
//     (the handler composes it; the spec has no direct shape), but every
//     property is spec-derived: "session" is complete_model_upload's
//     response, "model" the ModelRef component, "status"
//     get_model_status's response.
var catalog = []tool{
	{"whoami", op("get_me")},
	{"get_account_info", func(g *generator) (map[string]any, error) {
		return g.envelope(
			"get_account_info envelope. Each section is present exactly when requested "+
				"via include; an omitted include returns all three.",
			nil, // no required keys: include narrows the envelope to a subset
			prop{"usage", "get_usage"},
			prop{"quotas", "get_usage_quotas"},
			prop{"plan", "get_billing_plan"},
		)
	}},
	{"list_repos", op("list_repos")},
	{"get_repo", op("get_repo")},
	{"create_repo", op("create_repo")},
	{"update_repo", op("update_repo")},
	{"delete_repo", func(*generator) (map[string]any, error) {
		// Hand-authored: DELETE /v1/repos/{account}/{repo} answers 204 with no
		// body; the tool synthesizes this acknowledgement itself.
		return map[string]any{
			"type": "object",
			"description": "Synthesized by delete_repo: the API acknowledges the deletion " +
				"with 204 and no body.",
			"properties": map[string]any{
				"deleted": map[string]any{"type": "boolean", "const": true},
				"repo":    map[string]any{"type": "string", "description": "The deleted repository, as ACCOUNT/NAME."},
			},
			"required":             []any{"deleted", "repo"},
			"additionalProperties": false,
		}, nil
	}},
	{"list_models", op("list_models")},
	{"get_model", func(g *generator) (map[string]any, error) {
		model, err := g.op("get_model")
		if err != nil {
			return nil, err
		}
		withTargets, err := g.envelope(
			"get_model envelope returned when include_targets is true.",
			[]any{"model", "targets"},
			prop{"model", "get_model"},
			prop{"targets", "list_model_targets"},
		)
		if err != nil {
			return nil, err
		}
		return anyOf(
			"The model alone, or with include_targets the {\"model\", \"targets\"} envelope.",
			model, withTargets)
	}},
	{"get_conversion_status", op("get_model_status")},
	{"set_default_model", op("set_default_model")},
	{"import_model", op("import_model")},
	{"get_deployment_info", func(g *generator) (map[string]any, error) {
		options, err := g.op("get_deployment_options")
		if err != nil {
			return nil, err
		}
		guide, err := g.op("get_deployment_guide")
		if err != nil {
			return nil, err
		}
		return anyOf(
			"The deployment options catalog (called with no arguments) or one model "+
				"version's deployment guide (called with repo and model_key).",
			options, guide)
	}},
	{"get_model_report", func(g *generator) (map[string]any, error) {
		var shapes []map[string]any
		for _, id := range []string{"get_general_report", "get_llm_report", "get_package_report"} {
			s, err := g.op(id)
			if err != nil {
				return nil, err
			}
			shapes = append(shapes, s)
		}
		return anyOf(
			"One of the three report shapes, selected by report_type: "+
				"general, llm, or package.",
			shapes...)
	}},
	{"search_library", func(g *generator) (map[string]any, error) {
		page, err := g.op("list_library_models")
		if err != nil {
			return nil, err
		}
		withProviders, err := g.envelope(
			"search_library envelope returned when include_providers is true.",
			[]any{"models", "providers"},
			prop{"models", "list_library_models"},
			prop{"providers", "list_library_providers"},
		)
		if err != nil {
			return nil, err
		}
		return anyOf(
			"The model page alone, or with include_providers the "+
				"{\"models\", \"providers\"} envelope.",
			page, withProviders)
	}},
	{"get_library_model", op("get_library_model")},
	{"request_model_download", op("create_download_authorization")},
	{"upload_model", func(g *generator) (map[string]any, error) {
		session, err := g.op("complete_model_upload")
		if err != nil {
			return nil, err
		}
		model, err := g.component("ModelRef")
		if err != nil {
			return nil, err
		}
		status, err := g.op("get_model_status")
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"type": "object",
			"description": "upload_model envelope: session is the raw upload-complete " +
				"response; model repeats its model reference when registration produced " +
				"one; status is the latest conversion status a wait_seconds poll observed.",
			"properties": map[string]any{
				"session": session,
				"model":   model,
				"status":  status,
			},
			"required":             []any{"session"},
			"additionalProperties": false,
		}, nil
	}},
}

// op builds a tool entry whose schema is exactly one operation's response.
func op(operationID string) func(g *generator) (map[string]any, error) {
	return func(g *generator) (map[string]any, error) { return g.op(operationID) }
}

// anyOf composes alternative shapes a tool returns depending on its arguments.
//
// The union carries a top-level "type": "object": the MCP spec types
// Tool.outputSchema as an object schema (schema.ts literally requires
// `type: "object"`), and clients such as Claude Code reject — and then drop —
// an entire catalog whose outputSchema lacks it. That top-level type is only
// sound when every branch is itself an object schema, so anyOf refuses any
// branch that is not.
func anyOf(description string, shapes ...map[string]any) (map[string]any, error) {
	alts := make([]any, len(shapes))
	for i, s := range shapes {
		if s["type"] != "object" {
			return nil, fmt.Errorf(
				"anyOf branch %d has type %v, not \"object\": a top-level \"type\":\"object\" would be unsound, and MCP outputSchema requires an object schema",
				i, s["type"])
		}
		alts[i] = s
	}
	return map[string]any{"type": "object", "description": description, "anyOf": alts}, nil
}

// prop names one envelope key and the operation whose response fills it.
type prop struct {
	key         string
	operationID string
}

// generator resolves operations against one parsed OpenAPI document.
type generator struct {
	doc        map[string]any
	components map[string]any
	// responses caches each operation's converted 2xx response schema, keyed
	// by operationId; envelope tools reference the same operation more than
	// once, and conversion is pure.
	responses map[string]map[string]any
}

func newGenerator(doc map[string]any) (*generator, error) {
	comps, ok := dig(doc, "components", "schemas")
	if !ok {
		return nil, errors.New("document has no components.schemas")
	}
	return &generator{doc: doc, components: comps, responses: map[string]map[string]any{}}, nil
}

// op returns the converted JSON schema of operationID's 2xx response. Every
// 2xx status must agree on one schema (import_model and
// create_download_authorization declare 200 and 201 with the same shape).
func (g *generator) op(operationID string) (map[string]any, error) {
	if cached, ok := g.responses[operationID]; ok {
		return deepCopy(cached), nil
	}
	operation, err := g.findOperation(operationID)
	if err != nil {
		return nil, err
	}
	responses, _ := dig(operation, "responses")
	var raw any
	for code, resp := range responses {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		respMap, ok := resp.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: response %s is not an object", operationID, code)
		}
		schema, ok := dig(respMap, "content", "application/json")
		if !ok {
			return nil, fmt.Errorf("%s: response %s has no application/json content", operationID, code)
		}
		if raw != nil && !reflect.DeepEqual(raw, schema["schema"]) {
			return nil, fmt.Errorf("%s: 2xx responses disagree on a schema", operationID)
		}
		raw = schema["schema"]
	}
	if raw == nil {
		return nil, fmt.Errorf("%s: no 2xx application/json response schema", operationID)
	}
	converted, err := g.convert(raw, operationID)
	if err != nil {
		return nil, err
	}
	object, ok := converted.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: response schema is not an object schema", operationID)
	}
	g.responses[operationID] = object
	return deepCopy(object), nil
}

// component returns the converted JSON schema of one named component, for
// hand-authored envelopes whose property is a spec component rather than an
// operation response.
func (g *generator) component(name string) (map[string]any, error) {
	converted, err := g.convert(map[string]any{"$ref": refPrefix + name}, "component "+name)
	if err != nil {
		return nil, err
	}
	object, ok := converted.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("component %s is not an object schema", name)
	}
	return object, nil
}

// findOperation locates one operation by its operationId.
func (g *generator) findOperation(operationID string) (map[string]any, error) {
	paths, ok := dig(g.doc, "paths")
	if !ok {
		return nil, errors.New("document has no paths")
	}
	for _, item := range paths {
		pathItem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for _, op := range pathItem {
			operation, ok := op.(map[string]any)
			if !ok {
				continue
			}
			if operation["operationId"] == operationID {
				return operation, nil
			}
		}
	}
	return nil, fmt.Errorf("operationId %q not found in the spec", operationID)
}

// strippedKeys are OpenAPI-isms and annotations that are not JSON Schema
// assertions and would only confuse consumers of the tool schema. None occur
// in the current spec; stripping is defensive against future regenerations.
var strippedKeys = map[string]bool{
	"xml":           true,
	"example":       true,
	"examples":      true,
	"discriminator": true,
	"externalDocs":  true,
}

// maxDepth bounds ref inlining. The components graph is acyclic today; a
// future cycle would otherwise inline forever.
const maxDepth = 50

// convert rewrites one OpenAPI 3.0 schema node as self-contained JSON Schema
// draft 2020-12: refs inlined, nullable unions restored, OpenAPI-only keys
// stripped. It never mutates its input.
func (g *generator) convert(node any, path string) (any, error) {
	return g.convertDepth(node, path, 0)
}

func (g *generator) convertDepth(node any, path string, depth int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%s: $ref nesting exceeds %d levels (cycle in components?)", path, maxDepth)
	}
	switch value := node.(type) {
	case map[string]any:
		// A bare $ref inlines to its (converted) target. Sibling keys would
		// be silently ignored by many validators, so they are an error.
		if ref, ok := value["$ref"]; ok {
			if len(value) != 1 {
				return nil, fmt.Errorf("%s: $ref with sibling keys is not supported", path)
			}
			target, name, err := g.refTarget(ref, path)
			if err != nil {
				return nil, err
			}
			return g.convertDepth(target, path+"→"+name, depth+1)
		}

		if value["nullable"] == true {
			return g.convertNullable(value, path, depth)
		}

		out := make(map[string]any, len(value))
		for key, item := range value {
			if strippedKeys[key] {
				continue
			}
			if key == "nullable" {
				// nullable with any value other than the literal true handled
				// above is a spec bug worth failing on, not dropping.
				return nil, fmt.Errorf("%s: unexpected nullable value %v", path, item)
			}
			converted, err := g.convertDepth(item, path+"/"+key, depth)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			converted, err := g.convertDepth(item, fmt.Sprintf("%s/%d", path, i), depth)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	default:
		return node, nil
	}
}

// convertNullable restores the union the source 3.1 spec expressed before the
// down-converter flattened it to `nullable: true` (see tools/openapi30):
//
//   - {type: T, nullable: true, ...} → {type: [T, "null"], ...}; an enum
//     additionally gains null, since an enum is exhaustive in JSON Schema.
//   - {allOf: [$ref], nullable: true} → {anyOf: [<inlined ref>, {type: null}]}.
func (g *generator) convertNullable(value map[string]any, path string, depth int) (any, error) {
	if allOf, ok := value["allOf"].([]any); ok {
		if len(allOf) != 1 || len(value) != 2 {
			return nil, fmt.Errorf("%s: nullable allOf is not a single bare $ref wrapper", path)
		}
		branch, err := g.convertDepth(allOf[0], path+"/allOf/0", depth)
		if err != nil {
			return nil, err
		}
		return map[string]any{"anyOf": []any{branch, map[string]any{"type": "null"}}}, nil
	}

	typ, ok := value["type"].(string)
	if !ok {
		return nil, fmt.Errorf("%s: nullable schema without a single type", path)
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		if key == "nullable" || strippedKeys[key] {
			continue
		}
		converted, err := g.convertDepth(item, path+"/"+key, depth)
		if err != nil {
			return nil, err
		}
		out[key] = converted
	}
	out["type"] = []any{typ, "null"}
	if enum, ok := out["enum"].([]any); ok {
		out["enum"] = append(append([]any{}, enum...), nil)
	}
	return out, nil
}

// refTarget resolves a local component reference.
func (g *generator) refTarget(ref any, path string) (any, string, error) {
	refStr, ok := ref.(string)
	if !ok {
		return nil, "", fmt.Errorf("%s: non-string $ref", path)
	}
	name, found := strings.CutPrefix(refStr, refPrefix)
	if !found {
		return nil, "", fmt.Errorf("%s: unsupported $ref %q (only %s* is handled)", path, refStr, refPrefix)
	}
	target, ok := g.components[name]
	if !ok {
		return nil, "", fmt.Errorf("%s: $ref to unknown component %q", path, name)
	}
	return target, name, nil
}

// envelope builds a composite tool's wrapper object whose properties are
// per-operation response schemas.
func (g *generator) envelope(description string, required []any, props ...prop) (map[string]any, error) {
	properties := make(map[string]any, len(props))
	for _, p := range props {
		schema, err := g.op(p.operationID)
		if err != nil {
			return nil, err
		}
		properties[p.key] = schema
	}
	out := map[string]any{
		"type":                 "object",
		"description":          description,
		"properties":           properties,
		"additionalProperties": false,
	}
	if required != nil {
		out["required"] = required
	}
	return out, nil
}

// writeAll renders every catalog schema deterministically (sorted keys — maps
// marshal sorted — two-space indent, no HTML escaping) into dir, replacing
// whatever *.json files were there so a renamed tool cannot leave a stale
// schema behind.
func (g *generator) writeAll(dir string) error {
	names := make(map[string]bool, len(catalog))
	for _, entry := range catalog {
		if names[entry.name] {
			return fmt.Errorf("catalog names %q twice", entry.name)
		}
		names[entry.name] = true
	}
	// Duplicate-check against the fixture mapping internal/mcp exports: every
	// tool a contract fixture maps to must have a generated schema here, so a
	// tool renamed in one place cannot leave the other pointing at nothing.
	for fixture, toolName := range mcp.FixtureTool {
		if !names[toolName] {
			return fmt.Errorf(
				"internal/mcp.FixtureTool maps fixture %q to tool %q, which is not in this catalog",
				fixture, toolName)
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	stale, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, f := range stale {
		if err := os.Remove(f); err != nil {
			return err
		}
	}

	for _, entry := range catalog {
		schema, err := entry.build(g)
		if err != nil {
			return fmt.Errorf("%s: %w", entry.name, err)
		}
		// MCP types Tool.outputSchema as an object schema — `"type": "object"`
		// at the top level is required, and clients (Claude Code among them)
		// drop the whole catalog over a tool that violates it. Enforced here so
		// no future build func can reintroduce a bare union or scalar schema.
		if schema["type"] != "object" {
			return fmt.Errorf(
				"%s: top-level type is %v, not \"object\"; MCP requires every outputSchema to be an object schema",
				entry.name, schema["type"])
		}
		schema["$schema"] = draft2020

		rendered, err := renderJSON(schema)
		if err != nil {
			return fmt.Errorf("%s: %w", entry.name, err)
		}
		if err := checkResolves(rendered); err != nil {
			return fmt.Errorf("%s: generated schema does not resolve: %w", entry.name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, entry.name+".json"), rendered, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// renderJSON marshals with two-space indentation, a trailing newline, and no
// HTML escaping, so spec prose like `<` and `&` survives verbatim and the
// files diff cleanly under gen-check.
func renderJSON(doc any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// checkResolves proves the rendered document is a schema the MCP server can
// register: it must unmarshal through the SDK's jsonschema package and
// resolve — the same steps mcp.AddTool performs, where a failure panics the
// server. Failing here turns that panic into a `make gen` error.
func checkResolves(rendered []byte) error {
	var s jsonschema.Schema
	if err := json.Unmarshal(rendered, &s); err != nil {
		return err
	}
	_, err := s.Resolve(nil)
	return err
}

// dig walks nested string-keyed objects.
func dig(node map[string]any, keys ...string) (map[string]any, bool) {
	current := node
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

// deepCopy clones a converted schema so envelope composition cannot alias one
// cached tree into two documents.
func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch value := v.(type) {
	case map[string]any:
		return deepCopy(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return v
	}
}
