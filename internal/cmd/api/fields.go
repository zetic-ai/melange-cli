package api

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// integerRE recognizes -F values that become JSON numbers.
var integerRE = regexp.MustCompile(`^-?[0-9]+$`)

// parseFields folds -f/--raw-field and -F/--field specs into one JSON-ready
// object, or nil when no fields were given. Raw fields stay strings; typed
// fields go through coerceValue. Standard input can back at most one @- value:
// it is a stream, so a second @- would silently read empty bytes.
func parseFields(raw, typed []string, stdin io.Reader) (map[string]any, error) {
	if len(raw)+len(typed) == 0 {
		return nil, nil
	}
	stdinConsumed := false
	readStdin := func() ([]byte, error) {
		if stdinConsumed {
			return nil, errors.New("standard input already consumed by a previous @- value")
		}
		stdinConsumed = true
		return io.ReadAll(stdin)
	}
	params := map[string]any{}
	for _, spec := range raw {
		if err := addField(params, spec, false, readStdin); err != nil {
			return nil, err
		}
	}
	for _, spec := range typed {
		if err := addField(params, spec, true, readStdin); err != nil {
			return nil, err
		}
	}
	return params, nil
}

// addField parses one key=value spec and merges it into params.
func addField(params map[string]any, spec string, typed bool, readStdin func() ([]byte, error)) error {
	key, rawValue, ok := strings.Cut(spec, "=")
	if !ok {
		return fmt.Errorf("invalid field %q: expected key=value", spec)
	}
	var value any = rawValue
	if typed {
		v, err := coerceValue(rawValue, readStdin)
		if err != nil {
			return fmt.Errorf("field %q: %w", spec, err)
		}
		value = v
	}

	base, sub, isArray, err := splitFieldKey(key)
	if err != nil {
		return err
	}
	switch {
	case isArray:
		switch existing := params[base].(type) {
		case nil:
			params[base] = []any{value}
		case []any:
			params[base] = append(existing, value)
		default:
			return fmt.Errorf("field %q: %q already holds a non-array value", spec, base)
		}
	case sub != "":
		switch existing := params[base].(type) {
		case nil:
			params[base] = map[string]any{sub: value}
		case map[string]any:
			existing[sub] = value
		default:
			return fmt.Errorf("field %q: %q already holds a non-object value", spec, base)
		}
	default:
		switch params[base].(type) {
		case []any, map[string]any:
			return fmt.Errorf("field %q: %q already holds a nested value", spec, base)
		default:
			params[base] = value
		}
	}
	return nil
}

// splitFieldKey parses the supported key forms: key, key[subkey], key[].
func splitFieldKey(key string) (base, sub string, isArray bool, err error) {
	open := strings.IndexByte(key, '[')
	if open == -1 {
		if key == "" || strings.ContainsRune(key, ']') {
			return "", "", false, fieldKeyError(key)
		}
		return key, "", false, nil
	}
	base, rest := key[:open], key[open:]
	if base == "" {
		return "", "", false, fieldKeyError(key)
	}
	switch {
	case rest == "[]":
		return base, "", true, nil
	case strings.HasSuffix(rest, "]") && !strings.ContainsAny(rest[1:len(rest)-1], "[]"):
		return base, rest[1 : len(rest)-1], false, nil
	default:
		return "", "", false, fieldKeyError(key)
	}
}

func fieldKeyError(key string) error {
	return fmt.Errorf("invalid field key %q: supported forms are key, key[subkey], and key[]", key)
}

// coerceValue applies -F's typed conversion: true/false/null and integers
// become JSON types, @path inserts a file's contents as a string (@- reads
// standard input via readStdin), anything else stays a string.
func coerceValue(v string, readStdin func() ([]byte, error)) (any, error) {
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if integerRE.MatchString(v) {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n, nil
		}
		return v, nil // out of int64 range: keep the exact digits as a string
	}
	if name, ok := strings.CutPrefix(v, "@"); ok {
		var data []byte
		var err error
		if name == "-" {
			data, err = readStdin()
		} else {
			data, err = os.ReadFile(name)
		}
		if err != nil {
			return nil, err
		}
		return string(data), nil
	}
	return v, nil
}

// parseHeaders converts -H 'Name: value' specs into a header map, rejecting
// any attempt to override Authorization: the stored credentials for the
// configured host are the only ones this command will ever send.
func parseHeaders(specs []string) (map[string]string, error) {
	headers := map[string]string{}
	for _, spec := range specs {
		name, value, ok := strings.Cut(spec, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("invalid header %q: expected 'Name: value'", spec)
		}
		if strings.EqualFold(name, "Authorization") {
			return nil, fmt.Errorf(
				"cannot override Authorization: melange api always authenticates with the stored credentials for the configured host")
		}
		headers[http.CanonicalHeaderKey(name)] = strings.TrimSpace(value)
	}
	return headers, nil
}

// appendQuery folds scalar and array fields into the path's query string
// (the GET behavior). Nested objects have no query representation.
func appendQuery(path string, params map[string]any) (string, error) {
	base, rawQuery, _ := strings.Cut(path, "?")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("invalid query string in path: %w", err)
	}
	for _, key := range slices.Sorted(maps.Keys(params)) {
		switch v := params[key].(type) {
		case map[string]any:
			return "", fmt.Errorf("field %q: nested fields cannot be used as GET query parameters", key)
		case []any:
			for _, item := range v {
				q.Add(key, queryString(item))
			}
		default:
			q.Add(key, queryString(v))
		}
	}
	return base + "?" + q.Encode(), nil
}

// queryString renders a coerced field value for a query parameter.
func queryString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
