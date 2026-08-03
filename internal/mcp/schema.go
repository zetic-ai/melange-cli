package mcp

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// Page-size policy shared by every list tool: an agent that omits `limit`
// gets a digestible page, and the schema refuses a page larger than the API
// will serve (the server clamps anything above 100).
const (
	defaultPageLimit = 30
	maxPageLimit     = 100
)

// inputSchemaFor infers a tool's input schema from its args struct — field
// names, optionality, and `jsonschema` description tags — then hands the
// property map to refine for the numeric bounds and defaults the tag syntax
// cannot express. Deriving from the struct keeps schema and handler from
// drifting apart.
//
// It panics on a type jsonschema cannot describe: that is a programming
// error at registration time, exactly like the SDK's own AddTool checks.
func inputSchemaFor[T any](refine func(props map[string]*jsonschema.Schema)) *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("mcp: input schema for %T: %v", *new(T), err))
	}
	if refine != nil {
		refine(s.Properties)
	}
	return s
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

// pageLimit resolves an omitted limit to the default page size. The schema
// rejects every explicit value outside 1..maxPageLimit, so zero means absent.
func pageLimit(limit int) int {
	if limit <= 0 {
		return defaultPageLimit
	}
	return limit
}
