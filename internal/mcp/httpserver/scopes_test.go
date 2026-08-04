package httpserver

import (
	"context"
	"encoding/json"
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
// can assert exactly which endpoints a tool call reached. Bearers with the
// zoa_ prefix get the OAuth response shape (token.aud present — mandatory
// per the backend contract MeVerifier's canary enforces); everything else
// gets the PAT shape (no aud key).
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
			aud := ""
			if strings.HasPrefix(token, "zoa_") {
				aud = `,"aud":null`
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"user":{"email":"e2e@example.com","nickname":"e2e"},`+
				`"account":{"name":"e2e","type":"personal"},`+
				`"token":{"name":"e2e-token","scopes":%s%s}}`, scopes, aud)
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
	require.NoError(t, err)
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(*sdk.TextContent)
	require.True(t, ok)
	return text.Text, res.IsError
}

// TestWriteScopeEnforcedEndToEnd drives create_repo through a real validated
// HTTP server for both credential kinds: a read-scoped OAuth bearer is
// refused as an RFC 6750 403 (whose remediation text reaches the SDK client
// verbatim — go-sdk surfaces a JSON-RPC error carried in a non-2xx body), a
// read-scoped PAT keeps the in-band tool-error refusal WITHOUT poisoning its
// session, and a write-scoped bearer goes through. Together this proves the
// verifier's scopes and grant kind survive into the scope gate in stateless
// streamable mode.
func TestWriteScopeEnforcedEndToEnd(t *testing.T) {
	const (
		oauthReaderToken = "zoa_reader_SECRETREADER"
		patReaderToken   = "ztp_reader_SECRETREADER"
		writerToken      = "zoa_writer_SECRETWRITER"
	)
	backend, requests := newScopedBackend(t, map[string]string{
		oauthReaderToken: `["read"]`,
		patReaderToken:   `["read"]`,
		writerToken:      `["write"]`, // as granted: write need not include read
	})
	logs := &syncBuffer{}
	_, ts := newTestServer(t, backend.URL, func(cfg *Config) {
		cfg.ValidateTokens = true
		cfg.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	ctx := context.Background()

	// Read-scoped OAUTH bearer: the call fails as a protocol-level 403 whose
	// error carries the full agent-actionable remediation, and the backend
	// never sees the mutation — the only traffic is token validation itself.
	oauthReader := connectSession(t, ctx, ts.URL, oauthReaderToken)
	_, err := oauthReader.CallTool(ctx, &sdk.CallToolParams{
		Name: "create_repo", Arguments: map[string]any{"name": "whisper-tiny"},
	})
	require.Error(t, err, "a read-scoped OAuth token must draw the step-up 403")
	assert.Contains(t, err.Error(), `needs the "write" scope`)
	assert.Contains(t, err.Error(), `Re-authorize granting the "write" scope`,
		"the refusal must tell the agent how to fix it")
	assert.NotContains(t, err.Error(), oauthReaderToken, "refusals never carry bearer bytes")
	for _, line := range requests() {
		assert.NotEqual(t, "POST /v1/repos", line,
			"a scope refusal must make zero API mutations")
	}

	// Read-scoped PAT: the refusal stays IN-BAND — same remediation text, as
	// a tool error — because a PAT cannot step up and go-sdk clients treat a
	// non-2xx tool call as terminal for the connection. The follow-up whoami
	// on the SAME session is the anti-poisoning regression: one scope refusal
	// must not cost a PAT holder the rest of their session.
	patReader := connectSession(t, ctx, ts.URL, patReaderToken)
	text, isErr := callCreateRepo(t, ctx, patReader)
	require.True(t, isErr, "a read-scoped PAT must be refused as a tool error")
	assert.Contains(t, text, `needs the "write" scope`)
	assert.Contains(t, text, `Re-authorize granting the "write" scope`)
	assert.NotContains(t, text, patReaderToken, "refusals never carry bearer bytes")
	whoami, whoamiErr, err := callWhoami(ctx, patReader)
	require.NoError(t, err, "the session must survive a PAT scope refusal")
	assert.False(t, whoamiErr, "read tools must keep working after a PAT scope refusal: %s", whoami)
	for _, line := range requests() {
		assert.NotEqual(t, "POST /v1/repos", line,
			"a scope refusal must make zero API mutations")
	}

	// Write-scoped bearer: the same call succeeds and reaches the API.
	writer := connectSession(t, ctx, ts.URL, writerToken)
	text, isErr = callCreateRepo(t, ctx, writer)
	require.False(t, isErr, "a write-scoped token must pass the gate, got: %s", text)
	assert.Contains(t, requests(), "POST /v1/repos", "the permitted mutation must reach the API")

	// Credential-hygiene sweep over the new path: no bearer may appear in the
	// server log at debug level.
	captured := logs.String()
	for _, token := range []string{oauthReaderToken, patReaderToken, writerToken} {
		assert.NotContains(t, captured, token, "the bearer must never reach the server log")
	}
}

// createRepoCallBody is a raw tools/call request for the write-gated
// create_repo tool, id 7 so the 403 body's id echo is observable.
const createRepoCallBody = `{"jsonrpc":"2.0","id":7,"method":"tools/call",` +
	`"params":{"name":"create_repo","arguments":{"name":"whisper-tiny"}}}`

// TestInsufficientScope403Shape pins the RFC 6750 signal itself, the part a
// step-up-capable OAuth client machine-reads: HTTP 403 with
// `WWW-Authenticate: Bearer error="insufficient_scope", scope="write"`
// (plus the resource_metadata pointer when a resource is configured), and a
// JSON-RPC error body that echoes the request id and carries the same
// remediation text as the in-band tool error. The signal is reserved for
// OAuth grants — the PAT subtest pins that a ztp_ bearer with the identical
// grant keeps the 200 + in-band tool error (a PAT has no step-up flow, and
// go-sdk clients without one treat the 403 as terminal for the session).
func TestInsufficientScope403Shape(t *testing.T) {
	const oauthReaderToken = "zoa_reader_SECRETREADER"

	t.Run("validate-tokens posture", func(t *testing.T) {
		backend, requests := newScopedBackend(t, map[string]string{oauthReaderToken: `["read"]`})
		_, ts := newTestServer(t, backend.URL, func(cfg *Config) { cfg.ValidateTokens = true })

		resp := postMCP(t, ts.URL, oauthReaderToken, "", strings.NewReader(createRepoCallBody))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, `Bearer error="insufficient_scope", scope="write"`,
			resp.Header.Get("WWW-Authenticate"),
			"the challenge is the machine-readable step-up trigger (RFC 6750 §3.1)")

		var body struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int    `json:"id"`
			Error   struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Equal(t, "2.0", body.JSONRPC)
		assert.Equal(t, 7, body.ID, "the 403 body must echo the request id")
		assert.Contains(t, body.Error.Message, `needs the "write" scope`)
		assert.Contains(t, body.Error.Message, `Re-authorize granting the "write" scope`,
			"the 403 body must carry the same agent-actionable remediation as the tool error")
		assert.NotContains(t, body.Error.Message, oauthReaderToken)

		for _, line := range requests() {
			assert.NotEqual(t, "POST /v1/repos", line, "the refusal must make zero API mutations")
		}
	})

	t.Run("resource posture advertises discovery", func(t *testing.T) {
		backend, _ := newScopedBackend(t, map[string]string{oauthReaderToken: `["read"]`})
		_, ts := newTestServer(t, backend.URL, func(cfg *Config) {
			cfg.Resource = "https://mcp.zetic.ai"
		})

		resp := postMCP(t, ts.URL, oauthReaderToken, "", strings.NewReader(createRepoCallBody))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, `Bearer error="insufficient_scope", scope="write", `+
			`resource_metadata="https://mcp.zetic.ai/.well-known/oauth-protected-resource"`,
			resp.Header.Get("WWW-Authenticate"),
			"a 403 must let a step-up client discover the authorization server, like the 401 does")
	})

	t.Run("PAT keeps the in-band refusal", func(t *testing.T) {
		const patReaderToken = "ztp_reader_SECRETREADER"
		backend, requests := newScopedBackend(t, map[string]string{patReaderToken: `["read"]`})
		_, ts := newTestServer(t, backend.URL, func(cfg *Config) { cfg.ValidateTokens = true })

		resp := postMCP(t, ts.URL, patReaderToken, "", strings.NewReader(createRepoCallBody))
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"a scope-blocked PAT must not draw the 403 it cannot act on")
		assert.Empty(t, resp.Header.Get("WWW-Authenticate"))
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"isError":true`,
			"the PAT refusal is the in-band tool error")
		assert.Contains(t, string(raw), `Re-authorize granting the \"write\" scope`,
			"the same remediation text rides the in-band path")
		for _, line := range requests() {
			assert.NotEqual(t, "POST /v1/repos", line, "the refusal must make zero API mutations")
		}
	})
}

// TestPassthroughNeverEmits403 pins the PR2 trap's guard on the new signal:
// under PassthroughVerifier there is no verified grant, so the gate must not
// exist — a write tools/call with any bearer reaches the API, the sole
// authority in that posture.
func TestPassthroughNeverEmits403(t *testing.T) {
	backend, requests := newScopedBackend(t, map[string]string{"any-token": `["read"]`})
	_, ts := newTestServer(t, backend.URL, nil) // no ValidateTokens, no Resource

	resp := postMCP(t, ts.URL, "any-token", "", strings.NewReader(createRepoCallBody))
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"passthrough must never 403 on scopes it cannot know")
	assert.Empty(t, resp.Header.Get("WWW-Authenticate"))
	assert.Contains(t, requests(), "POST /v1/repos",
		"the API stays the sole authority under passthrough")
}

// TestValidatedTokenWithoutScopesPassesGate pins the other unenforced case
// requireScope documents: a validated token whose grant names no scopes falls
// back to the API's own authorization — the gate narrows known grants, it
// never invents an authority the backend didn't state.
func TestValidatedTokenWithoutScopesPassesGate(t *testing.T) {
	const token = "ztp_unscoped_SECRET"
	backend, requests := newScopedBackend(t, map[string]string{token: `[]`})
	_, ts := newTestServer(t, backend.URL, func(cfg *Config) { cfg.ValidateTokens = true })

	resp := postMCP(t, ts.URL, token, "", strings.NewReader(createRepoCallBody))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, requests(), "POST /v1/repos")
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
