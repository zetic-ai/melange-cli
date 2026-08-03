package httpserver

import (
	"crypto/sha256"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// In-process token-bucket rate limiter for the MCP endpoint.
//
// Per-instance by design: with N replicas behind a load balancer each
// instance enforces its own budget, so a client's effective ceiling is up to
// N× the constants. That is the accepted PR2 trade-off — this limiter is an
// overload guard for one instance, not a distributed quota; account-level
// limits stay with the Melange API. /healthz is never rate limited (it is
// routed before the protected chain in New).

const (
	// rateLimitPerMinute/rateLimitBurst: 120 sustained requests per minute
	// (2/s refill) with bursts of 40 per client key. Constants for now;
	// flags/env can arrive later per the PR2 plan.
	rateLimitPerMinute = 120
	rateLimitBurst     = 40
	// rateLimitRefillPerSecond is the bucket refill rate derived from
	// rateLimitPerMinute.
	rateLimitRefillPerSecond = float64(rateLimitPerMinute) / 60

	// maxRateLimitBuckets bounds the bucket map so a key spray (token
	// rotation, spoofed IPs) cannot grow memory without limit.
	maxRateLimitBuckets = 4096
	// bucketIdleTTL is when an untouched bucket becomes evictable. A bucket
	// refills completely in rateLimitBurst/rateLimitRefillPerSecond = 20s of
	// silence, after which it is indistinguishable from a fresh one — so
	// evicting it is lossless.
	bucketIdleTTL = 20 * time.Second
)

// rateLimiter is a token-bucket limiter over dynamic client keys.
type rateLimiter struct {
	mu sync.Mutex
	// now is the limiter clock; tests inject a fake. Read and written only
	// under mu.
	now     func() time.Time
	buckets map[string]*bucket
}

// bucket is one client's token bucket. Refill is computed lazily from the
// time elapsed since lastSeen instead of by a background ticker, so an idle
// limiter costs nothing.
type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// newRateLimiter builds a limiter; now == nil selects the real clock.
func newRateLimiter(now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{now: now, buckets: make(map[string]*bucket)}
}

// allow spends one token from key's bucket. When the bucket is empty it
// returns false plus the integral seconds (≥1, ceiling) until a token exists
// — the Retry-After value.
func (l *rateLimiter) allow(key string) (ok bool, retryAfter int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, found := l.buckets[key]
	if !found {
		l.evictLocked(now)
		b = &bucket{tokens: rateLimitBurst}
		l.buckets[key] = b
	} else {
		b.tokens = min(rateLimitBurst, b.tokens+now.Sub(b.lastSeen).Seconds()*rateLimitRefillPerSecond)
	}
	b.lastSeen = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := int(math.Ceil((1 - b.tokens) / rateLimitRefillPerSecond))
	if wait < 1 {
		wait = 1
	}
	return false, wait
}

// evictLocked keeps the bucket map bounded before a new bucket is inserted.
// Idle buckets (fully refilled, see bucketIdleTTL) are dropped losslessly;
// if a live spray still holds the map at the cap, arbitrary buckets go too.
// An evicted live key merely restarts with a fresh burst — no worse than the
// rotation the sprayer is already doing, and bounded memory wins.
func (l *rateLimiter) evictLocked(now time.Time) {
	if len(l.buckets) < maxRateLimitBuckets {
		return
	}
	for k, b := range l.buckets {
		if now.Sub(b.lastSeen) >= bucketIdleTTL {
			delete(l.buckets, k)
		}
	}
	for k := range l.buckets {
		if len(l.buckets) < maxRateLimitBuckets {
			break
		}
		delete(l.buckets, k)
	}
}

// middleware enforces the limit on next, answering 429 with an integral
// Retry-After when a bucket runs dry. It runs behind RequireBearerToken (see
// New for the order rationale), so every request reaching it carries a
// well-formed bearer and the key is in practice always the token hash; the
// remote-IP fallback in clientKey is defense in depth against a future chain
// reordering.
func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, retryAfter := l.allow(clientKey(r))
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			http.Error(w, "rate limit exceeded, retry later", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientKey derives the limiter key: SHA-256 of the bearer token when one is
// present (never the raw token — limiter state must be credential-free, same
// hygiene as the MeVerifier cache), else the remote IP. The prefixes keep the
// two namespaces from ever colliding.
func clientKey(r *http.Request) string {
	if token, ok := parseBearer(r.Header.Get("Authorization")); ok {
		sum := sha256.Sum256([]byte(token))
		return "t:" + string(sum[:])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}
