// Package fixturetest provides the shared helpers for tests that exercise the
// backend contract fixtures under openapi/fixtures: loading a fixture,
// concretizing its placeholder tokens, comparing JSON structurally with null
// and absent treated as equal, and matching request paths whose variable
// segments are placeholders.
//
// Both internal/contract (generated-client round-trips) and internal/mcp
// (tool round-trips through a real MCP session) build on this package, so the
// two suites agree on what "matches the fixture" means.
package fixturetest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Fixture is one shared contract fixture: the request the backend documented
// for an operation and the exact response its TestClient produced.
type Fixture struct {
	Request  Request  `json:"request"`
	Response Response `json:"response"`
}

// Request is the documented request half of a fixture. Path may carry
// placeholder segments (for example "<uuid>") standing in for per-run values.
type Request struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// Response is the recorded response half of a fixture.
type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// Dir returns the absolute path of the shared fixture directory, located
// relative to this source file so tests pass regardless of working directory.
func Dir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate caller for fixtures dir")
	}
	// internal/fixturetest/fixturetest.go -> repo root.
	root := filepath.Join(filepath.Dir(file), "..", "..")
	dir := filepath.Join(root, "openapi", "fixtures")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fixtures dir %s: %v", dir, err)
	}
	return dir
}

// Load reads and decodes one fixture by its stem name (without ".json").
func Load(t *testing.T, name string) Fixture {
	t.Helper()
	path := filepath.Join(Dir(t), name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	var fx Fixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("decoding fixture %s: %v", name, err)
	}
	return fx
}

// samples maps each normalization placeholder to a type-valid sample the
// generated structs can unmarshal (time.Time, enums, plain strings). The value
// only has to be well-formed for its Go type; the round-trip compares the
// concretized input against the re-marshaled output, so the exact sample is
// irrelevant as long as it survives the round-trip unchanged.
var samples = map[string]string{
	"<id>":         "00000000000000000000000000000000",
	"<uuid>":       "00000000000000000000000000000000",
	"<datetime>":   "2026-01-01T00:00:00Z",
	"<request_id>": "req_sample",
	"<signed-url>": "https://storage.example/sample?sig=1",
	"<target_id>":  "tm_1",
}

// Concretize replaces every placeholder token that appears inside JSON string
// values with its type-valid sample, so typed Go fields can unmarshal it.
// Placeholders only ever occupy (or are embedded in) JSON strings, so a plain
// token substitution on the raw JSON text is exact and reversible.
func Concretize(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(ConcretizeString(string(raw)))
}

// ConcretizeString is Concretize for plain text: it substitutes placeholder
// tokens in s, for example to turn the "<uuid>" segment of a fixture path into
// the concrete path parameter a client call needs.
func ConcretizeString(s string) string {
	for token, sample := range samples {
		s = strings.ReplaceAll(s, token, sample)
	}
	return s
}

// Canonicalize decodes JSON into a generic tree with nulls dropped and every
// number coerced to float64, so a comparison of two canonicalized trees treats
// (a) an explicit null and an absent key as equal — a nullable field the
// backend renders null but a generated `omitempty` field drops — and (b)
// numeric-representation artifacts as equal — JSON has no int/float
// distinction, so a fixture `1.0` and Go's re-marshaled `1` must compare
// equal.
func Canonicalize(raw []byte) (any, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return dropNulls(v), nil
}

func dropNulls(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = dropNulls(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = dropNulls(val)
		}
		return out
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return t
		}
		return f
	default:
		return v
	}
}

// pathSegments splits a URL path (dropping any query string) into segments.
func pathSegments(p string) []string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	return strings.Split(strings.Trim(p, "/"), "/")
}

// PathMatches compares a fixture path (whose variable segments are placeholder
// tokens like "<uuid>") against an actual path segment-by-segment, treating any
// "<...>" fixture segment as a wildcard.
func PathMatches(fixturePath, actualPath string) bool {
	want := pathSegments(fixturePath)
	got := pathSegments(actualPath)
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if strings.HasPrefix(want[i], "<") && strings.HasSuffix(want[i], ">") {
			continue // wildcard segment
		}
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

// SegmentAfter returns the path segment immediately following marker in the
// fixture path (e.g. the model key after "models"), or "" if absent.
func SegmentAfter(p, marker string) string {
	segs := pathSegments(p)
	for i, s := range segs {
		if s == marker && i+1 < len(segs) {
			return segs[i+1]
		}
	}
	return ""
}

// RepoCoords pulls the {account, repo} pair out of a "/v1/repos/{a}/{r}/..."
// fixture path. The values are placeholder-free literals from the fixtures
// (stable account/repo names), so they drive the generated path builders.
func RepoCoords(p string) (account, repo string) {
	segs := pathSegments(p)
	for i, s := range segs {
		if s == "repos" && i+2 < len(segs) {
			return segs[i+1], segs[i+2]
		}
	}
	return "", ""
}

// LibraryCoords pulls the {account, repo} pair out of a
// "/v1/library/models/{account}/{repo}" fixture path (get_library_model). The
// repos-based RepoCoords does not apply to the library namespace.
func LibraryCoords(p string) (account, repo string) {
	segs := pathSegments(p)
	for i, s := range segs {
		if s == "models" && i+2 < len(segs) {
			return segs[i+1], segs[i+2]
		}
	}
	return "", ""
}
