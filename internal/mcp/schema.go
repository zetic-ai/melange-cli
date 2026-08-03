package mcp

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// Page-size policy shared by every list tool: an agent that omits `limit`
// gets a digestible page, and the schema refuses a page larger than the API
// will serve (the server clamps anything above 100).
const (
	defaultPageLimit = 30
	maxPageLimit     = 100
)

// inputSchemas memoizes each args type's refined input schema
// (reflect.Type -> *inputSchemaEntry) so reflection and refinement run once
// per process, not once per server. As with outputSchemas, the stable pointer
// is what lets the SDK's pointer-keyed mcp.SchemaCache reuse the resolved
// schema across the HTTP transport's per-request servers.
var inputSchemas sync.Map

// inputSchemaEntry pairs a memoized schema with the identity of the refine
// function it was built with, so a second registration of the same args type
// under a different refinement fails loudly instead of silently receiving the
// first caller's bounds.
type inputSchemaEntry struct {
	schema *jsonschema.Schema
	refine uintptr
}

// inputSchemaFor infers a tool's input schema from its args struct — field
// names, optionality, and `jsonschema` description tags — then hands the
// property map to refine for the numeric bounds and defaults the tag syntax
// cannot express. Deriving from the struct keeps schema and handler from
// drifting apart. Every call for the same args type returns the same pointer;
// the args type is the schema's identity, so each tool must keep its own args
// struct (all of them do — that is also what stops handlers sharing arg
// shapes accidentally).
//
// It panics on a type jsonschema cannot describe, or on a repeat call whose
// refine differs from the memoized one: both are programming errors at
// registration time, exactly like the SDK's own AddTool checks.
func inputSchemaFor[T any](refine func(props map[string]*jsonschema.Schema)) *jsonschema.Schema {
	key := reflect.TypeFor[T]()
	if cached, ok := inputSchemas.Load(key); ok {
		return cached.(*inputSchemaEntry).use(key, refine)
	}
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("mcp: input schema for %T: %v", *new(T), err))
	}
	if refine != nil {
		refine(s.Properties)
	}
	// LoadOrStore converges concurrent first callers on one winner, so the
	// pointer handed out is stable even when two requests race the first
	// derivation.
	cached, _ := inputSchemas.LoadOrStore(key, &inputSchemaEntry{schema: s, refine: refineID(refine)})
	return cached.(*inputSchemaEntry).use(key, refine)
}

// use returns the memoized schema after checking the caller's refine matches
// the one the schema was built with.
func (e *inputSchemaEntry) use(key reflect.Type, refine func(props map[string]*jsonschema.Schema)) *jsonschema.Schema {
	if e.refine != refineID(refine) {
		panic(fmt.Sprintf("mcp: input schema for %s requested with a different refine than it was built with; give the tool its own args type", key))
	}
	return e.schema
}

// refineID is a refine function's comparable identity: its code pointer, or 0
// for nil. Top-level functions (every refine in this package) have exactly one
// code pointer per function.
func refineID(refine func(props map[string]*jsonschema.Schema)) uintptr {
	if refine == nil {
		return 0
	}
	return reflect.ValueOf(refine).Pointer()
}

// withPageBounds constrains the limit/offset properties every list tool
// shares, so an out-of-range page size is rejected before any API call.
//
// The default page size is applied by pageLimit rather than a schema
// `default`: the SDK panics while filling defaults into a request that sends
// a literal `"arguments": null`, and a malformed request must never take the
// server down.
func withPageBounds(props map[string]*jsonschema.Schema) {
	minLimit, maxLimit := 1.0, float64(maxPageLimit)
	props["limit"].Minimum = &minLimit
	props["limit"].Maximum = &maxLimit

	minOffset := 0.0
	props["offset"].Minimum = &minOffset
}

// enumValues renders a tool argument's accepted set for a schema `enum`,
// taking the generated API constants themselves so a spec change that drops
// or renames a value fails to compile instead of silently advertising a value
// the API no longer accepts.
func enumValues[T ~string](values ...T) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// pageLimit resolves an omitted limit to the default page size. The schema
// rejects every explicit value outside 1..maxPageLimit, so zero means absent.
func pageLimit(limit int) int {
	if limit <= 0 {
		return defaultPageLimit
	}
	return limit
}
