package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimiterBurstThenRefill(t *testing.T) {
	clk := newFakeClock()
	l := newRateLimiter(clk.Now)

	// The full burst passes back to back.
	for i := 0; i < rateLimitBurst; i++ {
		ok, _ := l.allow("k")
		require.True(t, ok, "request %d is within the burst", i+1)
	}

	// The next request is denied with an integral Retry-After: 0.5s to the
	// next token at 2/s rounds up to 1.
	ok, retryAfter := l.allow("k")
	require.False(t, ok, "request past the burst must be denied")
	assert.Equal(t, 1, retryAfter)

	// One second refills two tokens (120/min), no more.
	clk.Advance(1 * time.Second)
	for i := 0; i < 2; i++ {
		ok, _ := l.allow("k")
		require.True(t, ok, "refilled token %d must be spendable", i+1)
	}
	ok, retryAfter = l.allow("k")
	require.False(t, ok, "refill is 2 tokens/second, not more")
	assert.Equal(t, 1, retryAfter)

	// Twenty seconds of silence restores the full burst — and no further
	// silence can overfill it.
	clk.Advance(bucketIdleTTL)
	for i := 0; i < rateLimitBurst; i++ {
		ok, _ := l.allow("k")
		require.True(t, ok, "token %d of the restored burst", i+1)
	}
	ok, _ = l.allow("k")
	require.False(t, ok, "the bucket must cap at the burst size")
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	clk := newFakeClock()
	l := newRateLimiter(clk.Now)

	for i := 0; i < rateLimitBurst; i++ {
		ok, _ := l.allow("client-a")
		require.True(t, ok)
	}
	ok, _ := l.allow("client-a")
	require.False(t, ok, "client-a exhausted its bucket")

	ok, _ = l.allow("client-b")
	assert.True(t, ok, "client-b's bucket is untouched by client-a's exhaustion")
}

// TestRateLimiterBoundedMemory pins the eviction contract: the bucket map
// never exceeds maxRateLimitBuckets, and idle (fully refilled) buckets are
// dropped to make room for new clients.
func TestRateLimiterBoundedMemory(t *testing.T) {
	clk := newFakeClock()
	l := newRateLimiter(clk.Now)

	// A live key spray cannot push the map past the cap.
	for i := 0; i < maxRateLimitBuckets+512; i++ {
		l.allow(fmt.Sprintf("spray-%05d", i))
		l.mu.Lock()
		size := len(l.buckets)
		l.mu.Unlock()
		require.LessOrEqual(t, size, maxRateLimitBuckets,
			"bucket map must stay bounded during a key spray")
	}

	// Once every bucket has idled past refill, a single new client sweeps
	// them all: eviction of a fully-refilled bucket is lossless.
	clk.Advance(bucketIdleTTL)
	l.allow("fresh-client")
	l.mu.Lock()
	size := len(l.buckets)
	l.mu.Unlock()
	assert.Equal(t, 1, size, "idle buckets must be evicted, leaving only the live client")

	// The evicted-and-returned client simply starts a fresh burst.
	for i := 0; i < rateLimitBurst-1; i++ {
		ok, _ := l.allow("fresh-client")
		require.True(t, ok)
	}
	ok, _ := l.allow("fresh-client")
	require.False(t, ok, "fresh bucket still enforces the burst")
}

func TestClientKey(t *testing.T) {
	const token = "ztp_super_secret_0123456789"
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	key := clientKey(req)
	assert.True(t, strings.HasPrefix(key, "t:"), "bearer requests key on the token hash")
	assert.NotContains(t, key, token, "the limiter key must never contain raw token bytes")

	// Same token, different connection: same key (the client is the token).
	req2 := httptest.NewRequest(http.MethodPost, "/", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.RemoteAddr = "203.0.113.9:4321"
	assert.Equal(t, key, clientKey(req2))

	// No bearer: fall back to the remote IP, port stripped so one client's
	// ephemeral ports don't mint fresh buckets.
	anon := httptest.NewRequest(http.MethodPost, "/", nil)
	anon.RemoteAddr = "203.0.113.9:4321"
	assert.Equal(t, "ip:203.0.113.9", clientKey(anon))
}

// TestRateLimit429ThroughStack drives the full server: an over-burst client
// gets 429 + integral Retry-After, other tokens are unaffected, and /healthz
// is never limited.
func TestRateLimit429ThroughStack(t *testing.T) {
	stub := newMeStub(t, map[string]string{"token-a": "ana", "token-b": "bob"})
	srv, ts := newTestServer(t, stub.URL, nil)

	// Freeze the limiter clock before any traffic so refill cannot race the
	// test: exactly rateLimitBurst requests pass, then 429.
	srv.limiter.mu.Lock()
	srv.limiter.now = newFakeClock().Now
	srv.limiter.mu.Unlock()

	for i := 0; i < rateLimitBurst; i++ {
		resp := postMCP(t, ts.URL, "token-a", "", strings.NewReader(initializeBody))
		require.Equal(t, http.StatusOK, resp.StatusCode, "request %d is within the burst", i+1)
	}

	resp := postMCP(t, ts.URL, "token-a", "", strings.NewReader(initializeBody))
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "1", resp.Header.Get("Retry-After"),
		"429 must carry integral Retry-After seconds")

	// A different token has its own bucket.
	resp = postMCP(t, ts.URL, "token-b", "", strings.NewReader(initializeBody))
	assert.Equal(t, http.StatusOK, resp.StatusCode, "another client's bucket is independent")

	// The health probe is exempt: hammer it well past the burst.
	for i := 0; i < rateLimitBurst+10; i++ {
		hresp, err := http.Get(ts.URL + "/healthz")
		require.NoError(t, err)
		_ = hresp.Body.Close()
		require.Equal(t, http.StatusOK, hresp.StatusCode, "healthz must never be rate limited")
	}
}
