// Package programsheet narrows a partner program's benchmark sheets
// (GET /v1/programs/{program}/benchmarks) to one model and/or one SoC. The
// `melange program benchmarks` command and the get_program_benchmarks MCP
// tool share it, so both answer a filter the same way.
//
// Filtering works on the raw response: objects keep their key order and every
// value its exact bytes. The generated client decodes metric values as
// float32, so a decode/re-encode round trip through it would rewrite numbers
// such as 0.463; this package never does that.
package programsheet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Filter selects part of a program page. Empty fields select everything.
type Filter struct {
	// Model is a model slug (case-insensitive).
	Model string
	// SoC is a SoC code from the program's catalog (case-insensitive).
	SoC string
}

// Empty reports whether the filter selects the whole page.
func (f Filter) Empty() bool {
	return strings.TrimSpace(f.Model) == "" && strings.TrimSpace(f.SoC) == ""
}

// Apply returns body narrowed by f, or body itself when f is empty.
//
//   - Model keeps only that model in "models".
//   - SoC keeps only that SoC in "targets" and, in each model, only that
//     SoC's result; models with no result on it are dropped.
//
// "metrics" and the other top-level fields pass through unchanged. A model or
// SoC the page does not list is an error naming the ones it does.
func Apply(body []byte, f Filter) ([]byte, error) {
	if f.Empty() {
		return body, nil
	}
	page, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("decoding program benchmarks: %w", err)
	}
	program := page.stringField("program")

	if soc := strings.TrimSpace(f.SoC); soc != "" {
		targets, err := page.array("targets")
		if err != nil {
			return nil, err
		}
		kept, all, err := keep(targets, "soc", soc)
		if err != nil {
			return nil, err
		}
		if len(kept) == 0 {
			return nil, fmt.Errorf("SoC %q is not in the %s catalog; it lists %s",
				soc, program, listOrNone(all))
		}
		page.setArray("targets", kept)
	}

	models, err := page.array("models")
	if err != nil {
		return nil, err
	}
	if slug := strings.TrimSpace(f.Model); slug != "" {
		kept, all, err := keep(models, "slug", slug)
		if err != nil {
			return nil, err
		}
		if len(kept) == 0 {
			return nil, fmt.Errorf("model %q is not in the %s sheets; they list %s",
				slug, program, listOrNone(all))
		}
		models = kept
	}
	if soc := strings.TrimSpace(f.SoC); soc != "" {
		narrowed := make([]json.RawMessage, 0, len(models))
		for _, raw := range models {
			model, err := decodeObject(raw)
			if err != nil {
				return nil, fmt.Errorf("decoding a program model: %w", err)
			}
			results, err := model.array("results")
			if err != nil {
				return nil, err
			}
			kept, _, err := keep(results, "soc", soc)
			if err != nil {
				return nil, err
			}
			if len(kept) == 0 {
				continue
			}
			model.setArray("results", kept)
			encoded, err := model.MarshalJSON()
			if err != nil {
				return nil, err
			}
			narrowed = append(narrowed, encoded)
		}
		models = narrowed
	}
	page.setArray("models", models)
	return page.MarshalJSON()
}

// keep returns the elements whose string field equals want ignoring case,
// plus every element's value of that field in order (for the error message).
func keep(items []json.RawMessage, field, want string) (kept []json.RawMessage, all []string, err error) {
	for _, raw := range items {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, nil, fmt.Errorf("decoding program benchmarks: %w", err)
		}
		var value string
		if rawValue, ok := probe[field]; ok {
			if err := json.Unmarshal(rawValue, &value); err != nil {
				return nil, nil, fmt.Errorf("decoding program benchmarks %s: %w", field, err)
			}
		}
		all = append(all, value)
		if strings.EqualFold(value, want) {
			kept = append(kept, raw)
		}
	}
	return kept, all, nil
}

func listOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

// object is a JSON object that keeps its key order and raw member values.
type object []member

type member struct {
	key   string
	value json.RawMessage
}

func decodeObject(raw []byte) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("expected a JSON object")
	}
	var obj object
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("expected an object key")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		obj = append(obj, member{key: key, value: value})
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return obj, nil
}

func (o object) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.key == key {
			return m.value, true
		}
	}
	return nil, false
}

func (o object) stringField(key string) string {
	raw, ok := o.get(key)
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// array returns the elements of an array member; a missing or null member is
// an empty array.
func (o object) array(key string) ([]json.RawMessage, error) {
	raw, ok := o.get(key)
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decoding program benchmarks %s: %w", key, err)
	}
	return items, nil
}

// setArray replaces (or appends) an array member, keeping element bytes.
func (o *object) setArray(key string, items []json.RawMessage) {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(item)
	}
	b.WriteByte(']')
	for i := range *o {
		if (*o)[i].key == key {
			(*o)[i].value = b.Bytes()
			return
		}
	}
	*o = append(*o, member{key: key, value: b.Bytes()})
}

// MarshalJSON writes the members in their original order.
func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
