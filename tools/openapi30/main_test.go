package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeNullableUnionPreservesConstraints(t *testing.T) {
	doc := map[string]any{
		"openapi": "3.0.3",
		"components": map[string]any{"schemas": map[string]any{
			"Status": map[string]any{"properties": map[string]any{
				"stage": map[string]any{
					"title": "Stage",
					"anyOf": []any{
						map[string]any{"type": "string", "enum": []any{"convert", "benchmark"}},
						map[string]any{"type": "null"},
					},
				},
			}},
		}},
	}

	count, err := normalizeDocument(doc)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	stage := doc["components"].(map[string]any)["schemas"].(map[string]any)["Status"].(map[string]any)["properties"].(map[string]any)["stage"].(map[string]any)
	assert.Equal(t, true, stage["nullable"])
	assert.Equal(t, "string", stage["type"])
	assert.Equal(t, []any{"convert", "benchmark"}, stage["enum"])
	assert.Equal(t, "Stage", stage["title"])
	assert.NotContains(t, stage, "anyOf")
}

func TestNormalizeNullableRefUsesAllOfForOpenAPI30(t *testing.T) {
	doc := map[string]any{
		"openapi": "3.0.3",
		"schema": map[string]any{"anyOf": []any{
			map[string]any{"$ref": "#/components/schemas/Summary"},
			map[string]any{"type": "null"},
		}},
	}
	count, err := normalizeDocument(doc)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	schema := doc["schema"].(map[string]any)
	assert.Equal(t, true, schema["nullable"])
	assert.Equal(t, []any{map[string]any{"$ref": "#/components/schemas/Summary"}}, schema["allOf"])
	assert.NotContains(t, schema, "$ref")
}

func TestNormalizeRejectsResidualNullType(t *testing.T) {
	doc := map[string]any{"openapi": "3.0.3", "schema": map[string]any{"type": "null"}}
	_, err := normalizeDocument(doc)
	require.ErrorContains(t, err, "residual type:null")
}
