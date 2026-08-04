package httpserver

import (
	"bytes"
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
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// meBody renders a /v1/me response that satisfies the whoami output schema
// (user/account/token all required) for a distinct identity.
func meBody(name string) string {
	return fmt.Sprintf(`{"user":{"email":"%[1]s@example.com","nickname":"%[1]s"},`+
		`"account":{"name":"%[1]s","type":"personal"},`+
		`"token":{"name":"%[1]s-token","scopes":["read"]}}`, name)
}

// newMeStub serves GET /v1/me keyed by bearer token: each known token gets
// its own identity body, everything else gets a 401 authentication_error.
// The mapping is the isolation oracle: receiving identity X proves the
// outgoing request carried X's token, because no other token can produce it.
func newMeStub(t *testing.T, users map[string]string) *httptest.Server {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" {
			http.NotFound(w, r)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if name, known := users[token]; ok && known {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, meBody(name))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"unknown token"}}`)
	}))
	t.Cleanup(stub.Close)
	return stub
}

// newTestServer builds a Server against apiHost and serves its handler over
// httptest. mutate may adjust the Config before New.
func newTestServer(t *testing.T, apiHost string, mutate func(*Config)) (*Server, *httptest.Server) {
	t.Helper()
	cfg := Config{
		Listen:    "127.0.0.1:0",
		APIHost:   apiHost,
		UserAgent: "melange-cli-test/0.0.0",
		Version:   "v0.0.0-test",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := New(cfg)
	require.NoError(t, err)
	ts := httptest.NewServer(srv.handler)
	t.Cleanup(ts.Close)
	return srv, ts
}

// bearerRoundTripper injects a fixed Authorization bearer on every request.
type bearerRoundTripper struct {
	token string
}

func (b bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(req)
}

// connectSession opens a real SDK streamable session against url with the
// given bearer token.
func connectSession(t *testing.T, ctx context.Context, url, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "httpserver-test", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token}},
		// Stateless servers reject GET (405); the standalone SSE stream is
		// optional per spec, so skip it.
		DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// callWhoami invokes the whoami tool and returns its single text block.
func callWhoami(ctx context.Context, session *sdk.ClientSession) (string, bool, error) {
	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "whoami", Arguments: map[string]any{}})
	if err != nil {
		return "", false, err
	}
	if len(res.Content) != 1 {
		return "", res.IsError, fmt.Errorf("expected 1 content block, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		return "", res.IsError, fmt.Errorf("expected text content, got %T", res.Content[0])
	}
	return text.Text, res.IsError, nil
}

// initializeBody is a minimal, valid initialize request for raw POSTs.
const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"0.0.0"}}}`

// postMCP issues a raw streamable POST. token and origin are omitted when
// empty.
func postMCP(t *testing.T, url, token, origin string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestNewValidatesConfig(t *testing.T) {
	_, err := New(Config{APIHost: "https://api.zetic.ai"})
	assert.ErrorContains(t, err, "Listen")

	_, err = New(Config{Listen: ":0"})
	assert.ErrorContains(t, err, "APIHost")

	// ValidateTokens is real now (MeVerifier): a validating config constructs.
	srv, err := New(Config{Listen: ":0", APIHost: "https://api.zetic.ai", ValidateTokens: true})
	require.NoError(t, err)
	assert.NotNil(t, srv)

	// An invalid Resource is refused at construction — the audience an
	// operator declared must never be silently dropped or reshaped.
	_, err = New(Config{Listen: ":0", APIHost: "https://api.zetic.ai",
		Resource: "http://mcp.zetic.ai"})
	assert.ErrorContains(t, err, "Resource")

	// A valid Resource is stored in canonical form: the same value feeds the
	// audience comparison and the RFC 9728 metadata document, so equivalent
	// operator spellings must collapse to one identity.
	srv, err = New(Config{Listen: ":0", APIHost: "https://api.zetic.ai",
		Resource: "https://MCP.Zetic.AI/"})
	require.NoError(t, err)
	assert.Equal(t, "https://mcp.zetic.ai", srv.cfg.Resource)
}

func TestHealthzServesUnauthenticated(t *testing.T) {
	_, ts := newTestServer(t, "https://api.invalid", nil)

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "healthz must not require auth")
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	var got struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "ok", got.Status)
	assert.Equal(t, "v0.0.0-test", got.Version)
}

func TestMCPEndpointRequiresBearer(t *testing.T) {
	_, ts := newTestServer(t, "https://api.invalid", nil)

	resp := postMCP(t, ts.URL, "", "", strings.NewReader(initializeBody))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Malformed scheme is also rejected before any handler runs.
	req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(initializeBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

// TestPerRequestTokenIsolation is THE test of the per-request design: two
// concurrent sessions with different bearers must each reach the API with
// their own token on every call. The stub only produces identity X for
// token X, so a crossed credential would surface as the wrong identity (or a
// 401) in the other session's results.
func TestPerRequestTokenIsolation(t *testing.T) {
	stub := newMeStub(t, map[string]string{
		"token-alice": "alice",
		"token-bob":   "bob",
	})
	_, ts := newTestServer(t, stub.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	type party struct{ token, me, other string }
	parties := []party{
		{token: "token-alice", me: "alice@example.com", other: "bob@example.com"},
		{token: "token-bob", me: "bob@example.com", other: "alice@example.com"},
	}

	var wg sync.WaitGroup
	for _, p := range parties {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session := connectSession(t, ctx, ts.URL, p.token)
			for i := 0; i < 8; i++ {
				text, isErr, err := callWhoami(ctx, session)
				if !assert.NoError(t, err, "token %s call %d", p.token, i) {
					return
				}
				assert.False(t, isErr, "token %s call %d: unexpected tool error: %s", p.token, i, text)
				assert.Contains(t, text, p.me, "token %s must see its own identity", p.token)
				assert.NotContains(t, text, p.other, "token %s must never see the other identity", p.token)
			}
		}()
	}
	wg.Wait()
}

// TestRejectedBearerGetsHTTPHints proves getServer wires the HTTP AuthHints:
// an API 401 must carry the reconnect guidance, not the stdio 'melange auth
// login' text a remote client cannot act on.
func TestRejectedBearerGetsHTTPHints(t *testing.T) {
	stub := newMeStub(t, map[string]string{"token-alice": "alice"})
	_, ts := newTestServer(t, stub.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session := connectSession(t, ctx, ts.URL, "token-unknown")
	text, isErr, err := callWhoami(ctx, session)
	require.NoError(t, err)
	assert.True(t, isErr, "unknown token must produce a tool error")
	assert.Contains(t, text, "The Authorization bearer token was rejected")
	assert.Contains(t, text, "https://zetic.ai")
	assert.NotContains(t, text, "melange auth login")
	assert.NotContains(t, text, "MELANGE_API_KEY")
	assert.NotContains(t, text, "token-unknown", "the bearer itself must never appear in tool errors")
}

// TestStatelessNoSessionRequired pins the stateless posture: no session id is
// issued, and every fresh connection works without one.
func TestStatelessNoSessionRequired(t *testing.T) {
	stub := newMeStub(t, map[string]string{"token-a": "ana"})
	_, ts := newTestServer(t, stub.URL, nil)

	// Raw initialize: 200 without any Mcp-Session-Id, and none issued back.
	resp := postMCP(t, ts.URL, "token-a", "", strings.NewReader(initializeBody))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Mcp-Session-Id"), "stateless server must not issue session ids")

	// Two SDK sessions on fresh connections both complete a full tool call.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		session := connectSession(t, ctx, ts.URL, "token-a")
		text, isErr, err := callWhoami(ctx, session)
		require.NoError(t, err, "fresh connection %d", i)
		assert.False(t, isErr)
		assert.Contains(t, text, "ana@example.com")
		require.NoError(t, session.Close())
	}
}

// TestHTTPCatalogExcludesLocalTools pins the transport invariance the upload
// tool introduced: HTTP servers are built with EnableLocalTools false, so the
// catalog served to remote clients must never advertise upload_model — the
// server cannot see the caller's files, and the tool would silently read the
// SERVER's filesystem instead.
func TestHTTPCatalogExcludesLocalTools(t *testing.T) {
	stub := newMeStub(t, map[string]string{"token-a": "ana"})
	_, ts := newTestServer(t, stub.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connectSession(t, ctx, ts.URL, "token-a")

	var names []string
	var cursor string
	for {
		res, err := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		require.NoError(t, err)
		for _, tool := range res.Tools {
			names = append(names, tool.Name)
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	assert.Contains(t, names, "whoami", "the API-backed catalog is served")
	assert.NotContains(t, names, "upload_model",
		"local-only tools must stay hidden from the HTTP catalog")
}

func TestMaxRequestBodyBytesEnforced(t *testing.T) {
	_, ts := newTestServer(t, "https://api.invalid", nil)

	oversized := bytes.NewReader(bytes.Repeat([]byte("a"), maxRequestBodyBytes+1))
	resp := postMCP(t, ts.URL, "token-a", "", oversized)
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode,
		"oversized bodies must be rejected during the read, not buffered")
}

func TestOriginPolicy(t *testing.T) {
	stub := newMeStub(t, map[string]string{"token-a": "ana"})

	t.Run("default empty allowlist", func(t *testing.T) {
		_, ts := newTestServer(t, stub.URL, nil)

		// No Origin header: non-browser MCP clients pass.
		resp := postMCP(t, ts.URL, "token-a", "", strings.NewReader(initializeBody))
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Any browser origin is rejected, even with a valid bearer.
		resp = postMCP(t, ts.URL, "token-a", "https://evil.example", strings.NewReader(initializeBody))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("allowlisted origins", func(t *testing.T) {
		_, ts := newTestServer(t, stub.URL, func(c *Config) {
			c.AllowedOrigins = []string{"https://app.zetic.ai"}
		})

		resp := postMCP(t, ts.URL, "token-a", "https://app.zetic.ai", strings.NewReader(initializeBody))
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// Origins are matched case-insensitively (scheme and host are
		// case-insensitive per RFC 3986).
		resp = postMCP(t, ts.URL, "token-a", "HTTPS://APP.ZETIC.AI", strings.NewReader(initializeBody))
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		resp = postMCP(t, ts.URL, "token-a", "https://app.zetic.ai.evil.example", strings.NewReader(initializeBody))
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	// Origin never gates the health probe.
	t.Run("healthz ignores origin", func(t *testing.T) {
		_, ts := newTestServer(t, stub.URL, nil)
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://evil.example")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestDebugTransportDisabledInHTTPMode pins credential hygiene: MELANGE_DEBUG
// must not enable the API debug transport (which dumps Authorization headers)
// in HTTP mode, and the bearer must never reach the server logs.
func TestDebugTransportDisabledInHTTPMode(t *testing.T) {
	t.Setenv("MELANGE_DEBUG", "1")

	const secret = "super-secret-bearer-2718281828"
	stub := newMeStub(t, map[string]string{secret: "ana"})

	logs := &syncBuffer{}
	srv, ts := newTestServer(t, stub.URL, func(c *Config) {
		c.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})

	// The seam itself: per-request client options never carry a debug writer,
	// even with MELANGE_DEBUG set.
	assert.Nil(t, srv.apiOptions(secret).Debug,
		"HTTP mode must never enable the API debug transport: it dumps Authorization headers")

	// End to end: a full tool call with debug requested leaves no trace of
	// the bearer in the logs.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connectSession(t, ctx, ts.URL, secret)
	text, isErr, err := callWhoami(ctx, session)
	require.NoError(t, err)
	assert.False(t, isErr, "tool call failed: %s", text)

	assert.NotContains(t, logs.String(), secret, "bearer token leaked into server logs")
}

// TestShutdownDrainsInflightRequests exercises the shutdown contract with a
// real listener: cancellation lets the in-flight tool call finish, then
// ListenAndServe returns nil (exit 0).
func TestShutdownDrainsInflightRequests(t *testing.T) {
	inflight := make(chan struct{}, 1)
	release := make(chan struct{})
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inflight <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, meBody("ana"))
	}))
	t.Cleanup(stub.Close)

	srv, err := New(Config{
		Listen:    "127.0.0.1:0",
		APIHost:   stub.URL,
		UserAgent: "melange-cli-test/0.0.0",
		Version:   "v0.0.0-test",
	})
	require.NoError(t, err)

	serveCtx, stop := context.WithCancel(context.Background())
	defer stop()
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe(serveCtx) }()
	require.Eventually(t, func() bool { return srv.Addr() != nil },
		5*time.Second, 10*time.Millisecond, "server never bound its listener")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connectSession(t, ctx, "http://"+srv.Addr().String(), "token-a")

	type result struct {
		text  string
		isErr bool
		err   error
	}
	call := make(chan result, 1)
	go func() {
		text, isErr, err := callWhoami(ctx, session)
		call <- result{text, isErr, err}
	}()

	// Wait until the tool call is provably in flight (blocked in the API
	// stub), then begin shutdown and let the call complete.
	select {
	case <-inflight:
	case <-time.After(10 * time.Second):
		t.Fatal("tool call never reached the API stub")
	}
	stop()
	time.Sleep(50 * time.Millisecond) // let Shutdown begin draining
	close(release)

	select {
	case res := <-call:
		require.NoError(t, res.err, "in-flight tool call must complete during drain")
		assert.False(t, res.isErr, "in-flight tool call failed: %s", res.text)
		assert.Contains(t, res.text, "ana@example.com")
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight tool call did not complete during drain")
	}

	select {
	case err := <-served:
		assert.NoError(t, err, "clean drain must return nil (exit 0)")
	case <-time.After(10 * time.Second):
		t.Fatal("ListenAndServe did not return after drain")
	}
}

// TestStatelessRejectsGET pins the transport shape: stateless mode serves
// POST only, so there are no long-lived server streams to break multi-replica
// deployments.
func TestStatelessRejectsGET(t *testing.T) {
	_, ts := newTestServer(t, "https://api.invalid", nil)

	req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer token-a")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}
