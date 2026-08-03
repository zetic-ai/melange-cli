package httpserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/zetic-ai/melange-cli/internal/api"
)

// This file is the auth assembly point for the MCP endpoint. Two verifiers
// exist: PassthroughVerifier (the default relay posture — any non-empty
// bearer flows through to the Melange API, which is the real authority) and
// MeVerifier (--validate-tokens — bearers are checked against GET /v1/me
// before any tool runs). /healthz is never behind any of this.

// AuthMiddleware wraps the MCP handler with bearer authentication: the SDK's
// RequireBearerToken rejects requests without a well-formed bearer (401) and
// runs verifier on the token, then bearerToContext captures the raw bearer in
// the request context for the per-request ClientProvider. Every 401 leaves
// with a WWW-Authenticate challenge (see challengeWriter).
func AuthMiddleware(verifier auth.TokenVerifier, next http.Handler) http.Handler {
	require := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
		// Melange personal access tokens carry no in-band expiry; their
		// lifetime is managed server-side. Without this, the middleware would
		// 401 every TokenInfo whose Expiration is zero. When MeVerifier maps a
		// real expires_at into TokenInfo, the SDK still enforces it per
		// request.
		AllowMissingExpiration: true,
		// Seam (CLI-PR4): set ResourceMetadataURL here when the OAuth
		// protected-resource metadata endpoint (RFC 9728) lands. The SDK then
		// emits `WWW-Authenticate: Bearer resource_metadata="..."` itself and
		// challengeWriter's bare fallback goes dormant (it only fires when the
		// header is absent).
	})
	inner := require(bearerToContext(next))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(&challengeWriter{ResponseWriter: w}, r)
	})
}

// challengeWriter guarantees every 401 on the MCP endpoint carries a
// WWW-Authenticate challenge, as RFC 9110 §11.6.1 requires.
//
// go-sdk v1.7.0's RequireBearerToken only writes the header when
// ResourceMetadataURL or Scopes produce challenge parameters (auth/auth.go:
// empty params ⇒ no header at all), and we configure neither yet. This
// wrapper adds the bare `Bearer` scheme exactly when the SDK left the header
// unset, so a parameterized SDK header always wins once CLI-PR4 configures
// ResourceMetadataURL. The 401 decision itself — status, body — stays
// SDK-generated; only the missing challenge is supplied.
type challengeWriter struct {
	http.ResponseWriter
}

func (w *challengeWriter) WriteHeader(code int) {
	if code == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.NewResponseController reach the underlying writer (the
// streamable handler uses one to flush).
func (w *challengeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// PassthroughVerifier accepts any non-empty bearer token without validating
// it upstream. This is the default relay posture: the token's real check
// happens when the per-request API client presents it to the Melange API, and
// a bad token fails there as a tool error carrying the HTTP reconnect hints.
//
// Any token shape is accepted deliberately — ztp_ personal access tokens
// today, zoa_ OAuth access tokens in CLI-PR4, whatever the API mints next.
// The relay must never gatekeep token formats; only the API knows what a
// valid credential looks like.
func PassthroughVerifier(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	if token == "" {
		// Unreachable behind RequireBearerToken (its whitespace-split header
		// extraction cannot produce an empty token); kept so the verifier is
		// safe standing alone.
		return nil, fmt.Errorf("%w: empty bearer token", auth.ErrInvalidToken)
	}
	return &auth.TokenInfo{}, nil
}

const (
	// meCacheTTL bounds how long a positive /v1/me verification is reused.
	// Within the TTL a revoked token can still reach tools on this instance;
	// 60s keeps that window shorter than any human revocation flow while
	// collapsing per-request upstream calls to about one per minute per
	// token. The cache is per-instance and purely an optimization —
	// correctness never depends on it (multi-replica constraint).
	meCacheTTL = 60 * time.Second
	// meCacheMaxEntries bounds the cache so a token spray cannot grow memory
	// without limit. 1024 live entries ≈ a few hundred KB worst case.
	meCacheMaxEntries = 1024
)

// MeVerifier validates bearer tokens against GET /v1/me on the configured API
// host (the same host the tools call). Enabled by --validate-tokens: it
// rejects bad credentials at the door with a 401 instead of letting each tool
// call fail downstream.
//
// Positive results are cached for meCacheTTL keyed by SHA-256 of the token —
// raw token bytes are never held as map keys, so no cache dump or debugger
// snapshot can yield a credential. Negative results are never cached: a
// just-created token must work on the next request even if a stale attempt
// preceded it, and an attacker gains nothing from re-verification (each miss
// is one upstream 401).
type MeVerifier struct {
	// apiOptions builds the outgoing client options for one bearer; the
	// server passes (*Server).apiOptions so Debug is always nil (credential
	// hygiene) and host/UA/timeout match the tools.
	apiOptions func(bearer string) api.Options
	// now is the cache clock; tests inject a fake (package seam style, see
	// internal/wait). Guarded by mu so tests can swap it safely.
	now func() time.Time

	mu    sync.Mutex
	cache map[[sha256.Size]byte]meCacheEntry
}

type meCacheEntry struct {
	info    auth.TokenInfo
	expires time.Time
}

// NewMeVerifier builds a MeVerifier; its Verify method is the
// auth.TokenVerifier.
func NewMeVerifier(apiOptions func(bearer string) api.Options) *MeVerifier {
	return &MeVerifier{
		apiOptions: apiOptions,
		now:        time.Now,
		cache:      make(map[[sha256.Size]byte]meCacheEntry),
	}
}

// Verify implements auth.TokenVerifier: cached success, else one GET /v1/me
// with the presented bearer.
func (v *MeVerifier) Verify(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	key := sha256.Sum256([]byte(token))
	if info, ok := v.cached(key); ok {
		return info, nil
	}
	info, err := v.validate(ctx, token)
	if err != nil {
		return nil, err // negative results are never cached
	}
	v.store(key, *info)
	return info, nil
}

// validate performs the upstream check. Every error message here is static or
// status-derived by construction: the SDK writes err.Error() into the HTTP
// response body, so no upstream body text — and certainly no token bytes —
// may flow through.
func (v *MeVerifier) validate(ctx context.Context, token string) (*auth.TokenInfo, error) {
	client, err := api.NewClient(v.apiOptions(token))
	if err != nil {
		return nil, fmt.Errorf("building token validation client: %w", err)
	}
	g, err := client.Gen()
	if err != nil {
		return nil, fmt.Errorf("building token validation client: %w", err)
	}
	resp, err := g.GetMeWithResponse(ctx)
	if err != nil {
		// Transport failure: the token's validity is unknown, so this is not
		// ErrInvalidToken — the SDK answers 500, and the client retries
		// instead of discarding a possibly-good credential.
		return nil, fmt.Errorf("token validation request failed: %w", err)
	}
	switch {
	case resp.JSON200 != nil:
		info := &auth.TokenInfo{Scopes: resp.JSON200.Token.Scopes}
		if exp := resp.JSON200.Token.ExpiresAt; exp != nil {
			// A real expiry rides along so RequireBearerToken enforces it on
			// every request, including cache hits inside the TTL.
			info.Expiration = *exp
		}
		return info, nil
	case resp.StatusCode() == http.StatusUnauthorized || resp.StatusCode() == http.StatusForbidden:
		return nil, fmt.Errorf("%w: bearer token rejected by the Melange API", auth.ErrInvalidToken)
	default:
		return nil, fmt.Errorf("token validation failed: /v1/me returned status %d", resp.StatusCode())
	}
}

// cached returns a live cache entry for key, if any.
func (v *MeVerifier) cached(key [sha256.Size]byte) (*auth.TokenInfo, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.cache[key]
	if !ok || v.now().After(e.expires) {
		return nil, false
	}
	info := e.info
	return &info, true
}

// store inserts a positive result, holding the cache at meCacheMaxEntries:
// expired entries go first, then arbitrary live ones (map iteration order).
// Evicting a live entry only costs that token one extra /v1/me on its next
// request — the cache is never load-bearing.
func (v *MeVerifier) store(key [sha256.Size]byte, info auth.TokenInfo) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	if len(v.cache) >= meCacheMaxEntries {
		for k, e := range v.cache {
			if now.After(e.expires) {
				delete(v.cache, k)
			}
		}
		for k := range v.cache {
			if len(v.cache) < meCacheMaxEntries {
				break
			}
			delete(v.cache, k)
		}
	}
	v.cache[key] = meCacheEntry{info: info, expires: now.Add(meCacheTTL)}
}

// bearerContextKey keys the request's raw bearer token in the request
// context. Unexported: the only readers are in this package, and the token
// must never travel further than the outgoing Authorization header.
type bearerContextKey struct{}

// bearerToContext copies the request's bearer token into the request context
// so getServer can bind the per-request API client to it. It runs behind
// RequireBearerToken, which already rejected malformed credentials; a failed
// re-extraction here is defense in depth, not a reachable path.
func bearerToContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := parseBearer(r.Header.Get("Authorization"))
		if !ok {
			http.Error(w, "no bearer token", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), bearerContextKey{}, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// parseBearer extracts the token from an Authorization header value. It
// mirrors the SDK's own extraction (strings.Fields, case-insensitive scheme)
// so the two layers can never disagree about what the token is.
func parseBearer(header string) (string, bool) {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}
	return fields[1], true
}

// bearerFromRequest returns the bearer captured by bearerToContext.
func bearerFromRequest(r *http.Request) (string, bool) {
	token, ok := r.Context().Value(bearerContextKey{}).(string)
	return token, ok && token != ""
}
