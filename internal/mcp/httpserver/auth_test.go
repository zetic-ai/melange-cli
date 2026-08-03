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
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
)

// fakeClock is an injectable, advanceable clock for TTL and refill tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// The base time sits firmly in the past so a limiter clock frozen mid-test
// can never leap ahead of buckets created under the real clock (a positive
// jump would refill them and mask the 429 path).
func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// countingMeStub is newMeStub plus an atomic count of /v1/me hits, the oracle
// for the MeVerifier cache tests.
type countingMeStub struct {
	*httptest.Server
	calls atomic.Int64
}

func newCountingMeStub(t *testing.T, users map[string]string) *countingMeStub {
	t.Helper()
	s := &countingMeStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" {
			http.NotFound(w, r)
			return
		}
		s.calls.Add(1)
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
	t.Cleanup(s.Close)
	return s
}

// testAPIOptions mirrors (*Server).apiOptions for direct MeVerifier tests:
// Debug always nil, host fixed, token per bearer.
func testAPIOptions(host string) func(string) api.Options {
	return func(bearer string) api.Options {
		return api.Options{
			Host:      host,
			Token:     bearer,
			UserAgent: "melange-cli-test/0.0.0",
			Timeout:   5 * time.Second,
		}
	}
}

// newTestMeVerifier builds a MeVerifier against host with an injected clock.
func newTestMeVerifier(host string, clk *fakeClock) *MeVerifier {
	v := NewMeVerifier(testAPIOptions(host))
	v.now = clk.Now
	return v
}

func TestPassthroughVerifier(t *testing.T) {
	ctx := context.Background()
	// Any non-empty token passes, whatever its shape: ztp_ personal tokens,
	// zoa_ OAuth tokens (CLI-PR4 — this case is the regression pin), or
	// formats not invented yet. The relay must not gatekeep token formats.
	for _, token := range []string{
		"ztp_0123456789abcdef",
		"zoa_0123456789abcdef",
		"opaque-token-with-no-prefix",
	} {
		info, err := PassthroughVerifier(ctx, token, nil)
		require.NoError(t, err, "token %q must pass", token)
		require.NotNil(t, info)
	}

	_, err := PassthroughVerifier(ctx, "", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrInvalidToken, "empty bearer must be the SDK's 401, not a 500")
}

// TestZoaShapedTokenFlowsEndToEnd pins the no-prefix-gatekeeping contract
// through the whole stack: a zoa_-shaped OAuth token must reach the API and
// run tools, so CLI-PR4 cannot regress into a ztp_-only check.
func TestZoaShapedTokenFlowsEndToEnd(t *testing.T) {
	const zoa = "zoa_e2e_0123456789abcdef"
	stub := newMeStub(t, map[string]string{zoa: "ana"})
	_, ts := newTestServer(t, stub.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connectSession(t, ctx, ts.URL, zoa)
	text, isErr, err := callWhoami(ctx, session)
	require.NoError(t, err)
	assert.False(t, isErr, "zoa_-shaped token must run tools: %s", text)
	assert.Contains(t, text, "ana@example.com")
}

// TestUnauthenticated401Shape pins the 401 contract at the middleware seam:
// missing/empty/non-bearer credentials are rejected by the SDK middleware
// with a WWW-Authenticate Bearer challenge, and the protected handler is
// never invoked.
func TestUnauthenticated401Shape(t *testing.T) {
	handlerInvoked := false
	sentinel := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerInvoked = true })
	chain := AuthMiddleware(PassthroughVerifier, sentinel)

	cases := []struct {
		name          string
		authorization string
	}{
		{"missing header", ""},
		{"empty bearer", "Bearer "},
		{"wrong scheme", "Basic dXNlcjpwYXNz"},
		{"bare token no scheme", "ztp_0123456789abcdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handlerInvoked = false
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(initializeBody))
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			rec := httptest.NewRecorder()
			chain.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"),
				"401 must carry the RFC 9110 Bearer challenge")
			assert.False(t, handlerInvoked, "MCP handler must never run without credentials")
		})
	}
}

// TestUnauthenticated401ThroughStack verifies the same 401 shape over a real
// server (mux, origin middleware, streamable handler all in place).
func TestUnauthenticated401ThroughStack(t *testing.T) {
	_, ts := newTestServer(t, "https://api.invalid", nil)

	resp := postMCP(t, ts.URL, "", "", strings.NewReader(initializeBody))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))
}

func TestMeVerifierValidTokenRunsTools(t *testing.T) {
	stub := newMeStub(t, map[string]string{"token-alice": "alice"})
	_, ts := newTestServer(t, stub.URL, func(c *Config) { c.ValidateTokens = true })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connectSession(t, ctx, ts.URL, "token-alice")
	text, isErr, err := callWhoami(ctx, session)
	require.NoError(t, err)
	assert.False(t, isErr, "validated token must run tools: %s", text)
	assert.Contains(t, text, "alice@example.com")
}

// TestMeVerifierRejects401WithChallenge: with --validate-tokens an unknown
// bearer dies at the door — SDK 401 with the Bearer challenge — and the
// response never echoes the token.
func TestMeVerifierRejects401WithChallenge(t *testing.T) {
	const bad = "token-unknown-31415926"
	stub := newCountingMeStub(t, map[string]string{"token-alice": "alice"})
	_, ts := newTestServer(t, stub.URL, func(c *Config) { c.ValidateTokens = true })

	resp := postMCP(t, ts.URL, bad, "", strings.NewReader(initializeBody))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.NotContains(t, string(body), bad, "the bearer must never appear in the 401 body")
	assert.Equal(t, int64(1), stub.calls.Load(), "rejection happens at the door, before any tool path")
}

func TestMeVerifierMapsScopesAndExpiration(t *testing.T) {
	ctx := context.Background()

	t.Run("scopes, no expiry", func(t *testing.T) {
		stub := newCountingMeStub(t, map[string]string{"token-alice": "alice"})
		v := newTestMeVerifier(stub.URL, newFakeClock())
		info, err := v.Verify(ctx, "token-alice", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"read"}, info.Scopes, "scopes must map from the /v1/me token block")
		assert.True(t, info.Expiration.IsZero(), "absent expires_at maps to zero (AllowMissingExpiration)")
	})

	t.Run("expires_at maps to Expiration", func(t *testing.T) {
		expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
		stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"user":{"email":"e@example.com","nickname":"e"},`+
				`"account":{"name":"e","type":"personal"},`+
				`"token":{"name":"e-token","scopes":["read","write"],"expires_at":%q}}`,
				expires.Format(time.RFC3339))
		}))
		t.Cleanup(stub.Close)
		v := newTestMeVerifier(stub.URL, newFakeClock())
		info, err := v.Verify(ctx, "token-e", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"read", "write"}, info.Scopes)
		assert.True(t, expires.Equal(info.Expiration),
			"expires_at must ride into TokenInfo so the SDK enforces it per request")
	})
}

func TestMeVerifierCachesPositiveResults(t *testing.T) {
	ctx := context.Background()
	stub := newCountingMeStub(t, map[string]string{"token-alice": "alice"})
	clk := newFakeClock()
	v := newTestMeVerifier(stub.URL, clk)

	// Second verification inside the TTL is served from cache.
	for i := 0; i < 3; i++ {
		info, err := v.Verify(ctx, "token-alice", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"read"}, info.Scopes, "cache hits carry the full TokenInfo")
	}
	assert.Equal(t, int64(1), stub.calls.Load(), "verifications inside the TTL must not re-hit /v1/me")

	// Just inside the TTL: still cached.
	clk.Advance(meCacheTTL - time.Second)
	_, err := v.Verify(ctx, "token-alice", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(1), stub.calls.Load())

	// Past the TTL: revalidate upstream.
	clk.Advance(2 * time.Second)
	_, err = v.Verify(ctx, "token-alice", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(2), stub.calls.Load(), "an expired cache entry must be revalidated")
}

func TestMeVerifierNeverCachesNegatives(t *testing.T) {
	ctx := context.Background()
	stub := newCountingMeStub(t, map[string]string{})
	v := newTestMeVerifier(stub.URL, newFakeClock())

	for i := int64(1); i <= 3; i++ {
		_, err := v.Verify(ctx, "token-bad", nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, auth.ErrInvalidToken)
		assert.NotContains(t, err.Error(), "token-bad", "verifier errors must never carry the bearer")
		assert.Equal(t, i, stub.calls.Load(),
			"negative results must not be cached: a just-created token has to work next request")
	}
}

// TestMeVerifierUpstreamFailureIsNotInvalidToken: an API outage must not
// masquerade as a bad credential — the SDK turns non-ErrInvalidToken errors
// into a 500, telling the client to retry rather than discard its token.
func TestMeVerifierUpstreamFailureIsNotInvalidToken(t *testing.T) {
	ctx := context.Background()

	// Unexpected status (404: not retried by the API transport, unlike 5xx).
	stub := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(stub.Close)
	v := newTestMeVerifier(stub.URL, newFakeClock())
	_, err := v.Verify(ctx, "token-a", nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, auth.ErrInvalidToken)

	// Transport failure (connection refused).
	dead := httptest.NewServer(http.HandlerFunc(http.NotFound))
	dead.Close()
	v = newTestMeVerifier(dead.URL, newFakeClock())
	_, err = v.Verify(ctx, "token-a", nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, auth.ErrInvalidToken)
}

// TestMeVerifierCacheBounded pins the size bound under a token spray: the
// cache must never exceed meCacheMaxEntries live entries.
func TestMeVerifierCacheBounded(t *testing.T) {
	ctx := context.Background()
	// Accept every token so each distinct bearer becomes a positive entry.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, meBody("spray"))
	}))
	t.Cleanup(stub.Close)
	v := newTestMeVerifier(stub.URL, newFakeClock())

	for i := 0; i < meCacheMaxEntries+64; i++ {
		_, err := v.Verify(ctx, fmt.Sprintf("token-%04d", i), nil)
		require.NoError(t, err)

		v.mu.Lock()
		size := len(v.cache)
		v.mu.Unlock()
		require.LessOrEqual(t, size, meCacheMaxEntries,
			"cache must stay bounded while a token spray is in progress")
	}
}

// TestMeVerifierConcurrent exercises the cache under -race: many goroutines,
// overlapping tokens, all must verify successfully with the bound held.
func TestMeVerifierConcurrent(t *testing.T) {
	ctx := context.Background()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, meBody("conc"))
	}))
	t.Cleanup(stub.Close)
	clk := newFakeClock()
	v := newTestMeVerifier(stub.URL, clk)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				token := fmt.Sprintf("token-%d", i%10)
				info, err := v.Verify(ctx, token, nil)
				assert.NoError(t, err)
				assert.NotNil(t, info)
				if i%7 == 0 {
					clk.Advance(10 * time.Second)
				}
			}
		}()
	}
	wg.Wait()

	v.mu.Lock()
	size := len(v.cache)
	v.mu.Unlock()
	assert.LessOrEqual(t, size, meCacheMaxEntries)
}

// TestBearerNeverInLogs is THE credential-hygiene test: with debug-level
// logging capturing everything the server emits, a full mix of traffic —
// successful tool calls, token-validation rejections, and rate-limited
// requests — must leave zero bearer bytes in the logs.
func TestBearerNeverInLogs(t *testing.T) {
	const (
		good = "super-secret-good-bearer-1618033988"
		bad  = "super-secret-bad-bearer-2718281828"
	)
	stub := newMeStub(t, map[string]string{good: "ana"})

	logs := &syncBuffer{}
	srv, ts := newTestServer(t, stub.URL, func(c *Config) {
		c.ValidateTokens = true
		c.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})

	// Successful tool call with the good bearer.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connectSession(t, ctx, ts.URL, good)
	text, isErr, err := callWhoami(ctx, session)
	require.NoError(t, err)
	assert.False(t, isErr, "tool call failed: %s", text)

	// Rejected bearer (401 via MeVerifier).
	resp := postMCP(t, ts.URL, bad, "", strings.NewReader(initializeBody))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Rate-limited requests (429): freeze the limiter clock so the bucket
	// cannot refill, then overrun the burst.
	srv.limiter.mu.Lock()
	srv.limiter.now = newFakeClock().Now
	srv.limiter.mu.Unlock()
	saw429 := false
	for i := 0; i < rateLimitBurst+2; i++ {
		resp := postMCP(t, ts.URL, good, "", strings.NewReader(initializeBody))
		if resp.StatusCode == http.StatusTooManyRequests {
			saw429 = true
		}
	}
	require.True(t, saw429, "burst overrun must produce a 429 for the log-hygiene sweep")

	captured := logs.String()
	assert.NotContains(t, captured, good, "bearer token leaked into server logs")
	assert.NotContains(t, captured, bad, "rejected bearer leaked into server logs")
}
