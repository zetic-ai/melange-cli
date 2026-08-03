package httpserver

import (
	"crypto/sha256"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// In-process token-bucket rate limiting for the MCP endpoint. Two limiters
// run in series with different keys and budgets (see New for the chain):
// a coarse, IP-keyed one ahead of authentication, and the fine, token-keyed
// one behind it.
//
// Per-instance by design: with N replicas behind a load balancer each
// instance enforces its own budget, so a client's effective ceiling is up to
// N× the constants. That is the accepted PR2 trade-off — these limiters are
// overload guards for one instance, not a distributed quota; account-level
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

	// ipRateLimitPerMinute/ipRateLimitBurst tune the pre-auth limiter: 300
	// sustained requests per minute (5/s refill), bursts of 100, per source
	// IP. Deliberately generous relative to the per-token budget — a whole
	// NAT'd fleet of agents can share one source address, so this ceiling
	// must be invisible to legitimate traffic and only bite on machine-speed
	// sprays from a single host.
	ipRateLimitPerMinute       = 300
	ipRateLimitBurst           = 100
	ipRateLimitRefillPerSecond = float64(ipRateLimitPerMinute) / 60

	// maxRateLimitBuckets bounds each bucket map so a key spray (token
	// rotation, spoofed IPs) cannot grow memory without limit.
	maxRateLimitBuckets = 4096
	// bucketIdleTTL is when an untouched bucket becomes evictable. Both
	// policies refill completely within 20s of silence (burst/refill: 40/2
	// and 100/5), after which a bucket is indistinguishable from a fresh one
	// — so evicting it is lossless. TestLimitPoliciesRefillWithinIdleTTL
	// pins that relationship against future retuning.
	bucketIdleTTL = 20 * time.Second
)

// limitPolicy is one limiter's tuning: bucket size, refill rate, and the
// request attribute buckets are keyed on.
type limitPolicy struct {
	burst           float64
	refillPerSecond float64
	key             func(*http.Request) string
}

var (
	// tokenLimitPolicy is the fine-grained, post-auth budget. The key is the
	// bearer hash because the clients of this endpoint are tokens, not
	// addresses.
	tokenLimitPolicy = limitPolicy{
		burst:           rateLimitBurst,
		refillPerSecond: rateLimitRefillPerSecond,
		key:             clientKey,
	}
	// ipLimitPolicy is the coarse, pre-auth budget. The key is the peer
	// address because at that point in the chain nothing about the request is
	// trusted yet — see remoteIPKey.
	ipLimitPolicy = limitPolicy{
		burst:           ipRateLimitBurst,
		refillPerSecond: ipRateLimitRefillPerSecond,
		key:             remoteIPKey,
	}
)

// rateLimiter is a token-bucket limiter over dynamic client keys.
type rateLimiter struct {
	policy limitPolicy

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

// newRateLimiter builds the post-auth, token-keyed limiter; now == nil
// selects the real clock.
func newRateLimiter(now func() time.Time) *rateLimiter {
	return newLimiter(tokenLimitPolicy, now)
}

func newLimiter(policy limitPolicy, now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{policy: policy, now: now, buckets: make(map[string]*bucket)}
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
		b = &bucket{tokens: l.policy.burst}
		l.buckets[key] = b
	} else {
		b.tokens = min(l.policy.burst, b.tokens+now.Sub(b.lastSeen).Seconds()*l.policy.refillPerSecond)
	}
	b.lastSeen = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := int(math.Ceil((1 - b.tokens) / l.policy.refillPerSecond))
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
// Retry-After when a bucket runs dry.
func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, retryAfter := l.allow(l.policy.key(r))
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			http.Error(w, "rate limit exceeded, retry later", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientKey derives the post-auth limiter key: SHA-256 of the bearer token
// when one is present (never the raw token — limiter state must be
// credential-free, same hygiene as the MeVerifier cache), else the remote IP.
// The prefixes keep the two namespaces from ever colliding.
//
// This limiter runs behind RequireBearerToken (see New for the order
// rationale), so every request reaching it carries a well-formed bearer and
// the key is in practice always the token hash; the remote-IP fallback is
// defense in depth against a future chain reordering.
func clientKey(r *http.Request) string {
	if token, ok := parseBearer(r.Header.Get("Authorization")); ok {
		sum := sha256.Sum256([]byte(token))
		return "t:" + string(sum[:])
	}
	return remoteIPKey(r)
}

// remoteIPKey derives the pre-auth limiter key from the transport-level peer
// address, aggregated to the smallest unit that one host cannot multiply (see
// aggregateHost). The port is stripped so one client's ephemeral ports cannot
// mint fresh buckets.
//
// Proxy headers are deliberately ignored. On a directly-exposed listener
// X-Forwarded-For and friends are attacker-settable, so honoring them would
// hand a sprayer a free bucket per forged hop — the exact opposite of what
// this limiter is for. Seam for the infra PR: once the server runs behind a
// trusted L7 proxy (ALB), that PR must add explicit trusted-proxy
// configuration (a CIDR allowlist or a trusted hop count) and derive the
// client IP from the forwarding header only when the peer is that proxy;
// never unconditionally.
func remoteIPKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + aggregateHost(host)
}

// aggregateHost reduces a peer address to its limiter identity.
//
// IPv6 is keyed on the /64 prefix, not the address. A single IPv6 host is
// routinely given a whole /64 (SLAAC), and privacy extensions rotate its
// address natively — so per-address keying would let one machine mint ~2^64
// full-burst buckets and, worse, churn the bounded bucket map hard enough to
// evict the legitimate clients it is meant to protect. /64 is the smallest
// unit an end host cannot multiply, and it is what operators actually block
// on. The cost is that hosts sharing a /64 share a budget, which is the same
// trade IPv4 NAT already makes and why ipRateLimitPerMinute is generous.
//
// IPv4 keeps per-address keying (a /24 would sweep in unrelated networks),
// v4-mapped-v6 peers (::ffff:a.b.c.d, what a dual-stack listener reports for
// an IPv4 client) collapse to their IPv4 form so the two spellings cannot be
// two budgets, and an unparseable host keys on its literal text — the
// malformed-RemoteAddr fallback above must never fail open.
func aggregateHost(host string) string {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if addr.Is4() {
		return addr.String()
	}
	// PrefixFrom drops any zone, so a link-local peer cannot buy extra
	// buckets by varying the zone either.
	return netip.PrefixFrom(addr, 64).Masked().String()
}
