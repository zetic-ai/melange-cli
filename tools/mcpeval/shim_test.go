package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// brokenCatalog reproduces the real defect the shim compensates for: a
// tools/list result where some outputSchemas are bare anyOf unions with no
// top-level "type" (Claude Code 2.1.220 rejects the whole catalog on this).
const brokenCatalog = `{"jsonrpc":"2.0","id":2,"result":{"tools":[` +
	`{"name":"whoami","description":"d","outputSchema":{"type":"object","properties":{}}},` +
	`{"name":"search_library","description":"never import a library model just to read its public benchmarks","outputSchema":{"$schema":"s","anyOf":[{"type":"object","title":"Page"},{"type":"object","title":"Envelope"}],"description":"union"}}` +
	`]}}`

func TestNormalizeCatalogRepairsAnyOfUnions(t *testing.T) {
	out := normalizeCatalog([]byte(brokenCatalog))
	var envelope struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(out, &envelope))
	require.Len(t, envelope.Result.Tools, 2)

	repaired := envelope.Result.Tools[1]["outputSchema"].(map[string]any)
	assert.Equal(t, "object", repaired["type"], "the anyOf union must gain type:object")
	assert.Len(t, repaired["anyOf"], 2, "the union branches must survive")
	assert.Equal(t, "never import a library model just to read its public benchmarks",
		envelope.Result.Tools[1]["description"],
		"descriptions are the thing under evaluation and must pass through untouched")

	untouched := envelope.Result.Tools[0]["outputSchema"].(map[string]any)
	assert.Equal(t, "object", untouched["type"])
}

func TestNormalizeCatalogPassesThroughUntouched(t *testing.T) {
	cases := map[string]string{
		"not json":               `{{{`,
		"no result":              `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`,
		"result without tools":   `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"tools are great"}]}}`,
		"already object-typed":   `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"a","outputSchema":{"type":"object"}}]}}`,
		"anyOf with non-object":  `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"a","outputSchema":{"anyOf":[{"type":"object"},{"type":"array"}]}}]}}`,
		"no outputSchema at all": `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"a","description":"d"}]}}`,
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			in := []byte(msg)
			out := normalizeCatalog(in)
			assert.Equal(t, string(in), string(out),
				"anything the shim does not positively need to repair must pass through byte-identical")
		})
	}
}

func TestHTTPShimRewritesOnlyToolsList(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Fixture") {
		case "catalog":
			assert.Equal(t, "Bearer sekret", r.Header.Get("Authorization"),
				"the bearer must reach the real server untouched")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(brokenCatalog))
		case "challenge":
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {}\n\n"))
		}
	}))
	defer backend.Close()
	target, err := url.Parse(backend.URL)
	require.NoError(t, err)
	shim := httptest.NewServer(newHTTPShim(target))
	defer shim.Close()

	// tools/list JSON is repaired, and Content-Length stays consistent.
	req, _ := http.NewRequest(http.MethodPost, shim.URL, strings.NewReader(`{}`))
	req.Header.Set("X-Fixture", "catalog")
	req.Header.Set("Authorization", "Bearer sekret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Contains(t, string(body), `"type":"object"`)
	assert.NotEqual(t, brokenCatalog, string(body))
	assert.EqualValues(t, len(body), resp.ContentLength)

	// A 401 challenge passes through with its header intact.
	req, _ = http.NewRequest(http.MethodPost, shim.URL, strings.NewReader(`{}`))
	req.Header.Set("X-Fixture", "challenge")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))

	// Non-JSON content types are never buffered or modified.
	req, _ = http.NewRequest(http.MethodPost, shim.URL, strings.NewReader(`{}`))
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, "data: {}\n\n", string(body))
}

func TestStdioShimEndToEnd(t *testing.T) {
	// A stand-in server: emits one broken catalog line and one passthrough
	// line, then exits when stdin closes.
	script := `read line; printf '%s\n' '` + brokenCatalog + `'; printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"ok":true}}'; cat >/dev/null`
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var stdout, stderr strings.Builder
	code := runStdioShim([]string{"sh", "-c", script}, stdin, &stdout, &stderr)
	assert.Equal(t, 0, code, stderr.String())
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], `"type":"object"`, "the catalog line must be repaired in flight")
	assert.Contains(t, lines[1], `"ok":true`)
	assert.NotContains(t, lines[1], "type", "non-catalog lines pass through untouched")
}

func TestStdioShimMissingCommand(t *testing.T) {
	var stdout, stderr strings.Builder
	code := runStdioShim(nil, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "missing server command")
}
