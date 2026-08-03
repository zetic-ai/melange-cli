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

// TestLimitPoliciesRefillWithinIdleTTL pins the invariant eviction depends on:
// every policy's bucket must refill completely within bucketIdleTTL, or
// dropping an "idle" bucket would hand a throttled client a free burst
// instead of being lossless. A future retuning that breaks the relationship
// fails here rather than silently opening the limiter.
func TestLimitPoliciesRefillWithinIdleTTL(t *testing.T) {
	for name, p := range map[string]limitPolicy{"token": tokenLimitPolicy, "ip": ipLimitPolicy} {
		t.Run(name, func(t *testing.T) {
			full := time.Duration(p.burst / p.refillPerSecond * float64(time.Second))
			assert.LessOrEqual(t, full, bucketIdleTTL,
				"a %s bucket takes %s to refill, longer than the %s idle TTL: eviction would no longer be lossless",
				name, full, bucketIdleTTL)
		})
	}
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

// TestRemoteIPKeyIgnoresProxyHeaders pins the pre-auth key against the one
// mistake that would neuter the limiter: trusting a client-settable
// forwarding header would let a single sprayer mint a fresh bucket per forged
// hop.
func TestRemoteIPKeyIgnoresProxyHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "203.0.113.9:4321"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	req.Header.Set("X-Real-IP", "198.51.100.8")
	req.Header.Set("Forwarded", "for=198.51.100.9")

	assert.Equal(t, "ip:203.0.113.9", remoteIPKey(req),
		"the pre-auth key must come from the peer address, never a spoofable header")
}

// initializeMCP drives one full, valid MCP POST through the server handler
// from a chosen peer address. Going through the handler (not a socket) is
// what makes distinct client IPs expressible: httptest's real listener would
// report 127.0.0.1 for every request.
func initializeMCP(h http.Handler, remoteAddr, token string) *httptest.ResponseRecorder {
	return sprayRequest(h, remoteAddr, token, "application/json")
}

// TestRemoteIPKeyAggregatesIPv6 pins the unit the pre-auth limiter treats as
// one client. Keying IPv6 per address would be no limit at all: a single host
// holds a whole /64 (SLAAC) and privacy extensions rotate its address
// natively, so it could mint a full-burst bucket per request and churn the
// bounded map hard enough to evict everyone else. /64 is the smallest unit an
// end host cannot multiply.
func TestRemoteIPKeyAggregatesIPv6(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{"ipv4 keeps per-address keying", "203.0.113.9:4321", "ip:203.0.113.9"},
		{"ipv6 keys on the /64", "[2001:db8:1:2::1]:443", "ip:2001:db8:1:2::/64"},
		{"another address in that /64 is the same client",
			"[2001:db8:1:2:dead:beef:cafe:9]:443", "ip:2001:db8:1:2::/64"},
		{"a different /64 is a different client", "[2001:db8:1:3::1]:443", "ip:2001:db8:1:3::/64"},
		{"v4-mapped v6 keys like its ipv4 form", "[::ffff:203.0.113.9]:443", "ip:203.0.113.9"},
		{"a zone cannot buy a second budget", "[fe80::1%eth0]:443", "ip:fe80::/64"},
		{"an unparseable peer keys on its literal", "not-an-address", "ip:not-an-address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			assert.Equal(t, tt.want, remoteIPKey(req))
		})
	}

	// The v4-mapped form and the plain form must be one budget, not two.
	v4 := httptest.NewRequest(http.MethodPost, "/", nil)
	v4.RemoteAddr = "203.0.113.9:1111"
	mapped := httptest.NewRequest(http.MethodPost, "/", nil)
	mapped.RemoteAddr = "[::ffff:203.0.113.9]:2222"
	assert.Equal(t, remoteIPKey(v4), remoteIPKey(mapped),
		"a dual-stack listener spelling an IPv4 peer as v4-mapped must not double its budget")
}

// sprayMCP is initializeMCP's cheap twin, for the bulk of a burst where only
// the count matters. The wrong Content-Type makes the SDK answer 415 before
// it builds a per-request MCP server (verified in streamable.go: the
// media-type check precedes getServer), while the request still traverses
// every middleware this test cares about — IP limiter, Origin, auth, token
// limiter. Sending hundreds of full initializes instead would spend tens of
// seconds under -race constructing identical tool catalogs; each subtest
// still sends one real initializeMCP to pin that genuine MCP traffic is
// counted the same way.
func sprayMCP(h http.Handler, remoteAddr, token string) *httptest.ResponseRecorder {
	return sprayRequest(h, remoteAddr, token, "text/plain")
}

func sprayRequest(h http.Handler, remoteAddr, token, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(initializeBody))
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// exhaustIPBudget spends a peer's entire pre-auth budget and asserts the next
// request is throttled.
func exhaustIPBudget(t *testing.T, h http.Handler, peer string) {
	t.Helper()
	for i := 0; i < ipRateLimitBurst; i++ {
		require.NotEqual(t, http.StatusTooManyRequests,
			sprayMCP(h, peer, fmt.Sprintf("spray-token-%05d", i)).Code,
			"request %d is still within the IP burst", i+1)
	}
	require.Equal(t, http.StatusTooManyRequests, sprayMCP(h, peer, "one-too-many").Code,
		"the burst is spent, so this address must now be throttled")
}

// freezeIPLimiter pins the pre-auth limiter's clock so refill cannot race a
// spray: exactly ipRateLimitBurst requests pass, then 429.
func freezeIPLimiter(srv *Server) {
	srv.ipLimiter.mu.Lock()
	defer srv.ipLimiter.mu.Unlock()
	srv.ipLimiter.now = newFakeClock().Now
}

// TestIPRateLimitStopsTokenSpray is the reason the pre-auth limiter exists.
// The token-keyed limiter cannot throttle a caller who never reuses a token:
// every fresh bearer is a fresh bucket with a full burst. So a single host
// spraying unique bearers must be stopped by source address instead — in
// passthrough mode before it mints unlimited buckets, and in validate mode
// before it turns this server into an amplifier for /v1/me guessing.
func TestIPRateLimitStopsTokenSpray(t *testing.T) {
	const peer = "203.0.113.42:5555"

	t.Run("passthrough mode", func(t *testing.T) {
		stub := newMeStub(t, map[string]string{})
		srv, _ := newTestServer(t, stub.URL, nil)
		freezeIPLimiter(srv)

		// Every request carries a token never seen before, so the token
		// limiter is a no-op by construction: any 429 here is the IP limiter.
		// Real MCP traffic counts against the same budget as anything else.
		require.Equal(t, http.StatusOK, initializeMCP(srv.handler, peer, "spray-token-real").Code)
		for i := 1; i < ipRateLimitBurst; i++ {
			require.Equal(t, http.StatusUnsupportedMediaType,
				sprayMCP(srv.handler, peer, fmt.Sprintf("spray-token-%05d", i)).Code,
				"spray request %d is within the burst, so it is answered by the layers behind the limiter", i+1)
		}

		rec := sprayMCP(srv.handler, peer, "spray-token-final")
		assert.Equal(t, http.StatusTooManyRequests, rec.Code,
			"a unique-bearer spray from one address must be throttled by source IP")
		assert.Equal(t, "1", rec.Header().Get("Retry-After"),
			"429 must carry integral Retry-After seconds")

		// And the sprayer cannot buy itself out with a valid request either.
		assert.Equal(t, http.StatusTooManyRequests,
			initializeMCP(srv.handler, peer, "spray-token-real-2").Code)
	})

	t.Run("validate mode", func(t *testing.T) {
		stub := newCountingMeStub(t, map[string]string{})
		srv, _ := newTestServer(t, stub.URL, func(c *Config) { c.ValidateTokens = true })
		freezeIPLimiter(srv)

		for i := 0; i < ipRateLimitBurst; i++ {
			rec := sprayMCP(srv.handler, peer, fmt.Sprintf("spray-token-%05d", i))
			require.Equal(t, http.StatusUnauthorized, rec.Code, "spray request %d", i+1)
		}
		reached := stub.calls.Load()
		assert.Equal(t, int64(ipRateLimitBurst), reached,
			"each in-burst request costs exactly one upstream /v1/me (negatives are uncached)")

		// Past the burst the upstream must stop seeing the spray at all: the
		// 429 is served before the verifier runs.
		for i := 0; i < 25; i++ {
			rec := sprayMCP(srv.handler, peer, fmt.Sprintf("spray-token-over-%05d", i))
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
		}
		assert.Equal(t, reached, stub.calls.Load(),
			"throttled requests must never reach /v1/me: the pre-auth limiter runs ahead of the verifier")
	})

	t.Run("distinct client addresses are independent", func(t *testing.T) {
		stub := newMeStub(t, map[string]string{})
		srv, _ := newTestServer(t, stub.URL, nil)
		freezeIPLimiter(srv)
		exhaustIPBudget(t, srv.handler, peer)

		// A different source address is untouched and serves real MCP traffic.
		assert.Equal(t, http.StatusOK,
			initializeMCP(srv.handler, "198.51.100.7:9999", "innocent-token").Code,
			"one noisy address must not throttle everyone else")

		// A new ephemeral port on the noisy address is the same client, not a
		// fresh budget.
		assert.Equal(t, http.StatusTooManyRequests,
			sprayMCP(srv.handler, "203.0.113.42:6666", "another-token").Code)
	})

	t.Run("an IPv6 host cannot rotate addresses for a fresh budget", func(t *testing.T) {
		stub := newMeStub(t, map[string]string{})
		srv, _ := newTestServer(t, stub.URL, nil)
		freezeIPLimiter(srv)
		exhaustIPBudget(t, srv.handler, "[2001:db8:1:2::1]:5555")

		// A brand-new source address from the same /64 — what SLAAC privacy
		// extensions hand a sprayer for free — arrives already throttled.
		assert.Equal(t, http.StatusTooManyRequests,
			sprayMCP(srv.handler, "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:5555", "rotated-token").Code,
			"rotating within the /64 must not mint a fresh budget")

		// A genuinely different network is unaffected.
		assert.Equal(t, http.StatusOK,
			initializeMCP(srv.handler, "[2001:db8:1:3::1]:5555", "other-network-token").Code,
			"a different /64 is a different client")
	})

	t.Run("healthz stays exempt", func(t *testing.T) {
		stub := newMeStub(t, map[string]string{})
		srv, _ := newTestServer(t, stub.URL, nil)
		freezeIPLimiter(srv)
		exhaustIPBudget(t, srv.handler, peer)

		// The liveness probe shares the exhausted address: a throttled
		// neighbor must never make a deployment look unhealthy.
		for i := 0; i < 10; i++ {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.RemoteAddr = peer
			rec := httptest.NewRecorder()
			srv.handler.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, "healthz must never be rate limited")
		}
	})
}

// TestHealthzRejectsNonGET pins that the health path never falls through to
// the MCP chain: a probe sending the wrong method gets a method error, not a
// 401 that reads like a credential problem.
func TestHealthzRejectsNonGET(t *testing.T) {
	_, ts := newTestServer(t, "https://api.invalid", nil)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/healthz", strings.NewReader("{}"))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Allow"), "GET")

	// HEAD is served by the GET route (Go's ServeMux pairs them), so probes
	// that avoid bodies still work.
	headResp, err := http.Head(ts.URL + "/healthz")
	require.NoError(t, err)
	defer headResp.Body.Close()
	assert.Equal(t, http.StatusOK, headResp.StatusCode)
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
