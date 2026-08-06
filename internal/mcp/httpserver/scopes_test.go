package httpserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file pins per-tool scope enforcement END TO END through the real
// stateless streamable stack: RequireBearerToken stores the verifier's
// TokenInfo on the request context, the SDK seeds each per-request connection
// with that context, and the tool handler's requireScope gate reads it back.
// If any link in that chain broke — an SDK upgrade detaching handler contexts,
// the verifier no longer populating scopes — these tests fail.

// scopeRepoBody satisfies the create_repo output schema (mirrors the
// internal/mcp passthrough fixture body).
const scopeRepoBody = `{"name":"whisper-tiny","account":"zetic","full_name":"zetic/whisper-tiny",` +
	`"is_private":false,"model_type":"general","tags":["speech"],"description":"tiny",` +
	`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`

// newScopedBackend serves GET /v1/me with per-token scope grants and POST
// /v1/repos with a valid repo body, recording every request line so a test
// can assert exactly which endpoints a tool call reached.
func newScopedBackend(t *testing.T, scopesByToken map[string]string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lines = append(lines, r.Method+" "+r.URL.Path)
		mu.Unlock()
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case r.URL.Path == "/v1/me":
			scopes, known := scopesByToken[token]
			if !known {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"unknown token"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"user":{"email":"e2e@example.com","nickname":"e2e"},`+
				`"account":{"name":"e2e","type":"personal"},`+
				`"token":{"name":"e2e-token","scopes":%s}}`, scopes)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/repos":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, scopeRepoBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.Close)
	return stub, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

// callCreateRepo invokes create_repo and returns the result's single text
// block plus its error flag.
func callCreateRepo(t *testing.T, ctx context.Context, session *sdk.ClientSession) (string, bool) {
	t.Helper()
	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "create_repo", Arguments: map[string]any{"name": "whisper-tiny"},
	})
	require.NoError(t, err, "scope refusals are tool errors, never protocol errors")
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(*sdk.TextContent)
	require.True(t, ok)
	return text.Text, res.IsError
}

// TestWriteScopeEnforcedEndToEnd drives create_repo through a real validated
// HTTP server: a read-scoped bearer is refused before any API mutation while
// a write-scoped bearer goes through — proving the verifier's scopes survive
// into the tool handler's context in stateless streamable mode.
func TestWriteScopeEnforcedEndToEnd(t *testing.T) {
	const (
		readerToken = "ztp_reader_SECRETREADER"
		writerToken = "ztp_writer_SECRETWRITER"
	)
	backend, requests := newScopedBackend(t, map[string]string{
		readerToken: `["read"]`,
		writerToken: `["write"]`, // as granted: write need not include read
	})
	logs := &syncBuffer{}
	_, ts := newTestServer(t, backend.URL, func(cfg *Config) {
		cfg.ValidateTokens = true
		cfg.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	ctx := context.Background()

	// Read-scoped bearer: refused with remediation, and the backend never
	// sees the mutation — the only traffic is token validation itself.
	reader := connectSession(t, ctx, ts.URL, readerToken)
	text, isErr := callCreateRepo(t, ctx, reader)
	require.True(t, isErr, "a read-scoped token must be refused")
	assert.Contains(t, text, `needs the "write" scope`)
	assert.Contains(t, text, `Re-authorize granting the "write" scope`,
		"the refusal must tell the agent how to fix it")
	assert.NotContains(t, text, readerToken, "refusals never carry bearer bytes")
	for _, line := range requests() {
		assert.NotEqual(t, "POST /v1/repos", line,
			"a scope refusal must make zero API mutations")
	}

	// Write-scoped bearer: the same call succeeds and reaches the API.
	writer := connectSession(t, ctx, ts.URL, writerToken)
	text, isErr = callCreateRepo(t, ctx, writer)
	require.False(t, isErr, "a write-scoped token must pass the gate, got: %s", text)
	assert.Contains(t, requests(), "POST /v1/repos", "the permitted mutation must reach the API")

	// Credential-hygiene sweep over the new path: neither bearer may appear
	// in the server log at debug level.
	captured := logs.String()
	assert.NotContains(t, captured, readerToken, "the bearer must never reach the server log")
	assert.NotContains(t, captured, writerToken, "the bearer must never reach the server log")
}

// TestReadToolsUnaffectedByReadScope pins the other half of the contract: a
// read-scoped token still uses the read catalog normally.
func TestReadToolsUnaffectedByReadScope(t *testing.T) {
	const readerToken = "ztp_reader_SECRETREADER"
	backend, _ := newScopedBackend(t, map[string]string{readerToken: `["read"]`})
	_, ts := newTestServer(t, backend.URL, func(cfg *Config) { cfg.ValidateTokens = true })
	ctx := context.Background()

	session := connectSession(t, ctx, ts.URL, readerToken)
	text, isErr, err := callWhoami(ctx, session)
	require.NoError(t, err)
	require.False(t, isErr, "read tools must not require the write scope, got: %s", text)
	assert.Contains(t, text, "e2e@example.com")
}
