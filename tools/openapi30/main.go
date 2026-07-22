// Command openapi30 repairs and validates semantics that the pinned generic
// 3.1->3.0 converter does not preserve for oapi-codegen: nullable unions.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	input := flag.String("input", "", "down-converted OpenAPI 3.0 JSON")
	output := flag.String("output", "", "validated output JSON (may equal input)")
	flag.Parse()
	if *input == "" || *output == "" {
		fatal(errors.New("--input and --output are required"))
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		fatal(fmt.Errorf("decode %s: %w", *input, err))
	}
	count, err := normalizeDocument(doc)
	if err != nil {
		fatal(err)
	}
	if err := writeJSONAtomic(*output, doc); err != nil {
		fatal(err)
	}
	fmt.Printf("normalized and verified %d nullable schemas in %s\n", count, *output)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "openapi30:", err)
	os.Exit(1)
}

func normalizeDocument(doc map[string]any) (int, error) {
	version, _ := doc["openapi"].(string)
	if !strings.HasPrefix(version, "3.0.") {
		return 0, fmt.Errorf("expected an OpenAPI 3.0 document, got %q", version)
	}
	count := normalizeNode(doc)
	if path := residualNullType(doc, "$"); path != "" {
		return 0, fmt.Errorf("residual type:null at %s; conversion would lose nullability", path)
	}
	return count, nil
}

func normalizeNode(node any) int {
	switch value := node.(type) {
	case map[string]any:
		count := 0
		if nullable, ok := nullableBranch(value); ok {
			wrapper := make(map[string]any, len(value))
			for key, item := range value {
				if key != "anyOf" {
					wrapper[key] = item
				}
			}
			clear(value)
			if ref, ok := nullable["$ref"]; ok {
				value["allOf"] = []any{map[string]any{"$ref": ref}}
				for key, item := range nullable {
					if key != "$ref" {
						value[key] = item
					}
				}
			} else {
				for key, item := range nullable {
					value[key] = item
				}
			}
			for key, item := range wrapper {
				value[key] = item
			}
			value["nullable"] = true
			count++
		}
		for _, item := range value {
			count += normalizeNode(item)
		}
		return count
	case []any:
		count := 0
		for _, item := range value {
			count += normalizeNode(item)
		}
		return count
	default:
		return 0
	}
}

func nullableBranch(schema map[string]any) (map[string]any, bool) {
	branches, ok := schema["anyOf"].([]any)
	if !ok || len(branches) != 2 {
		return nil, false
	}
	var nonNull map[string]any
	nulls := 0
	for _, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		if len(branch) == 1 && branch["type"] == "null" {
			nulls++
		} else {
			nonNull = branch
		}
	}
	return nonNull, nulls == 1 && nonNull != nil
}

func residualNullType(node any, path string) string {
	switch value := node.(type) {
	case map[string]any:
		if value["type"] == "null" {
			return path
		}
		for key, item := range value {
			if found := residualNullType(item, path+"/"+key); found != "" {
				return found
			}
		}
	case []any:
		for index, item := range value {
			if found := residualNullType(item, fmt.Sprintf("%s/%d", path, index)); found != "" {
				return found
			}
		}
	}
	return ""
}

func writeJSONAtomic(path string, doc map[string]any) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".openapi30-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	encoder := json.NewEncoder(tmp)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
