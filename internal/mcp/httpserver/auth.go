package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/zetic-ai/melange-cli/internal/api"
)

// This file is the auth assembly point for the MCP endpoint. Two verifiers
// exist: PassthroughVerifier (the default relay posture — any non-empty
// bearer flows through to the Melange API, which is the real authority) and
// MeVerifier (--validate-tokens or a configured --resource — bearers are
// checked against GET /v1/me before any tool runs, with OAuth audience
// enforcement when a canonical resource URL is configured). /healthz is never
// behind any of this.

// AuthMiddleware wraps the MCP handler with bearer authentication: the SDK's
// RequireBearerToken rejects requests without a well-formed bearer (401) and
// runs verifier on the token, then bearerToContext captures the raw bearer in
// the request context for the per-request ClientProvider. Every 401 leaves
// with a WWW-Authenticate challenge (see challengeWriter).
//
// resourceMetadataURL is the absolute URL of this server's RFC 9728
// protected-resource metadata document, or "" when no resource identity is
// configured (the document is then not served at all — see metadata.go).
func AuthMiddleware(verifier auth.TokenVerifier, resourceMetadataURL string, next http.Handler) http.Handler {
	require := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
		// Melange personal access tokens carry no in-band expiry; their
		// lifetime is managed server-side. Without this, the middleware would
		// 401 every TokenInfo whose Expiration is zero. When MeVerifier maps a
		// real expires_at into TokenInfo, the SDK still enforces it per
		// request.
		AllowMissingExpiration: true,
		// Non-empty, the SDK stamps every 401 with the RFC 9728 discovery
		// pointer — `WWW-Authenticate: Bearer resource_metadata="<url>"` — the
		// entry point of the OAuth flow: challenge → metadata document →
		// authorization_servers[0] → RFC 8414 metadata → authorize/token.
		// Empty leaves the SDK header unset and challengeWriter supplies the
		// bare `Bearer` scheme instead.
		ResourceMetadataURL: resourceMetadataURL,
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
// empty params ⇒ no header at all). With a resource identity configured the
// SDK emits the parameterized challenge itself — it Adds the header before
// http.Error triggers WriteHeader, so this wrapper sees it and goes dormant.
// Without one, this wrapper adds the bare `Bearer` scheme exactly when the
// SDK left the header unset. Either way exactly one challenge leaves: the
// parameterized header is never overridden or duplicated. The 401 decision
// itself — status, body — stays SDK-generated; only the missing challenge is
// supplied.
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

// oauthGrantExtraKey marks a TokenInfo minted from the OAuth /v1/me response
// shape (token.aud key present — the same discriminator the zoa_ canary
// enforces). MeVerifier stamps it; scopeStepUpGate reads it to decide the
// SHAPE of a scope refusal: OAuth grants get the RFC 6750 403 they can act
// on, everything else keeps the in-band tool error.
const oauthGrantExtraKey = "melange.oauth_grant"

// isOAuthGrant reports whether info was minted from an OAuth-grant /v1/me
// shape (see oauthGrantExtraKey).
func isOAuthGrant(info *auth.TokenInfo) bool {
	if info == nil {
		return false
	}
	v, ok := info.Extra[oauthGrantExtraKey].(bool)
	return ok && v
}

// errValidationUnavailable is the single, static answer for every
// token-validation failure whose underlying error carries dynamic text. The
// SDK copies err.Error() verbatim into the HTTP 500 body (go-sdk v1.7.0
// auth/auth.go), so wrapping a *url.Error — or any error built from the
// configured API host — would publish the backend's hostname, resolved
// address, and net-layer detail to whoever holds a bearer-shaped string. The
// detail is logged server-side instead; the caller learns only that the check
// could not be completed, which is all it needs to retry.
var errValidationUnavailable = errors.New("token validation unavailable")

// MeVerifier validates bearer tokens against GET /v1/me on the configured API
// host (the same host the tools call). Enabled by --validate-tokens or by
// configuring --resource: it rejects bad credentials at the door with a 401
// instead of letting each tool call fail downstream.
//
// /v1/me is the authorization server's substitute for token introspection
// (there is no introspection endpoint by design): it accepts both credential
// kinds the API mints — ztp_ personal access tokens and zoa_ OAuth 2.1 access
// tokens — and 401s everything else, including zor_ refresh tokens. Both
// response shapes share the token block this verifier maps into
// auth.TokenInfo (scopes, expires_at); OAuth responses additionally carry the
// grant's RFC 8707 audience, which validate enforces against resource.
//
// Positive results are cached for meCacheTTL keyed by SHA-256 of the token —
// raw token bytes are never held as map keys, so no cache dump or debugger
// snapshot can yield a credential. Negative results are never cached: a
// just-created token must work on the next request even if a stale attempt
// preceded it, and an attacker gains nothing from re-verification (each miss
// is one upstream 401). Audience enforcement happens before the cache store,
// so every cached entry has already passed it for this verifier's resource
// (the resource is fixed at construction, never per request).
type MeVerifier struct {
	// apiOptions builds the outgoing client options for one bearer; the
	// server passes (*Server).apiOptions so Debug is always nil (credential
	// hygiene) and host/UA/timeout match the tools.
	apiOptions func(bearer string) api.Options
	// resource is this server's canonical resource URL in CanonicalResource
	// form, or "" when the operator configured none. Non-empty, it is the
	// audience OAuth tokens must be bound to; empty, the server has no
	// identity to compare against and audience enforcement is off (the plain
	// --validate-tokens posture).
	resource string
	// now is the cache clock; tests inject a fake (package seam style, see
	// internal/wait). Set at construction (or by test setup before first use)
	// and never mutated concurrently.
	now func() time.Time
	// logger receives the detail that must not reach the response body —
	// transport failures and client-construction faults. Never nil (the
	// constructor substitutes a discard handler) and never given a bearer.
	logger *slog.Logger

	mu    sync.Mutex
	cache map[[sha256.Size]byte]meCacheEntry
}

type meCacheEntry struct {
	info    auth.TokenInfo
	expires time.Time
}

// NewMeVerifier builds a MeVerifier; its Verify method is the
// auth.TokenVerifier. resource is the canonical resource URL audience-bound
// tokens must name (already in CanonicalResource form), or "" to skip
// audience enforcement. logger receives the failure detail that is withheld
// from callers; nil discards it.
func NewMeVerifier(apiOptions func(bearer string) api.Options, resource string, logger *slog.Logger) *MeVerifier {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &MeVerifier{
		apiOptions: apiOptions,
		resource:   resource,
		now:        time.Now,
		logger:     logger,
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

// validate performs the upstream check. Every error message returned here is
// static or status-derived by construction: the SDK writes err.Error() into
// the HTTP response body, so no upstream body text, no infrastructure detail
// (host, address, dial error) — and certainly no token bytes — may flow
// through. Anything dynamic goes to the logger instead.
func (v *MeVerifier) validate(ctx context.Context, token string) (*auth.TokenInfo, error) {
	client, err := api.NewClient(v.apiOptions(token))
	if err != nil {
		// Carries the configured API host (URL parse detail): log only.
		v.logger.Error("mcp token validation client build failed", "error", err)
		return nil, errValidationUnavailable
	}
	g, err := client.Gen()
	if err != nil {
		v.logger.Error("mcp token validation client build failed", "error", err)
		return nil, errValidationUnavailable
	}
	resp, err := g.GetMeWithResponse(ctx)
	if err != nil {
		// Transport failure: the token's validity is unknown, so this is not
		// ErrInvalidToken — the SDK answers 500, and the client retries
		// instead of discarding a possibly-good credential. The *url.Error
		// names the backend host and the dial fault, so it stays in the log.
		v.logger.Error("mcp token validation request failed", "error", err)
		return nil, errValidationUnavailable
	}
	switch {
	case resp.JSON200 != nil:
		aud, audPresent, err := meAudience(resp.Body)
		if err != nil {
			// Unreachable while the API keeps aud a string-or-null (the same
			// bytes just parsed as MeResponse); if the shape ever drifts, fail
			// closed without inventing a verdict on the token. The parse
			// detail is log-only like every other dynamic error here.
			v.logger.Error("mcp token validation response parse failed", "error", err)
			return nil, errValidationUnavailable
		}
		if !audPresent && strings.HasPrefix(token, "zoa_") {
			// Canary for the one silent failure mode of the audience control:
			// the backend contract (zetic/public/me.py) puts a token.aud key —
			// string or null — on EVERY zoa_ bearer's response; the key's
			// absence is the discriminator for the PAT shape. A zoa_ bearer
			// without it means the field was renamed or moved, and treating
			// that as "unbound" would silently disable audience enforcement
			// for every OAuth token. Fail closed instead (zoa_ acceptance and
			// the aud enrichment shipped together, so no real backend answers
			// a zoa_ bearer with the PAT shape). Static error out; detail —
			// never the bearer — to the log.
			v.logger.Error("mcp token validation contract drift: OAuth bearer answered without an aud field")
			return nil, errValidationUnavailable
		}
		if v.resource != "" && aud != "" && !resourceMatches(v.resource, aud) {
			// The token is real but bound to a different resource server
			// (RFC 8707): accepting it here would be the confused-deputy
			// passthrough the backend documents as OUR half of the contract
			// (/v1 itself does not enforce aud). The mismatch detail — no
			// credential, just the two resource URLs — goes to the operator
			// log; the caller gets a static 401.
			v.logger.Warn("mcp token validation audience mismatch",
				"aud", aud, "resource", v.resource)
			return nil, fmt.Errorf("%w: bearer token is bound to a different resource", auth.ErrInvalidToken)
		}
		info := &auth.TokenInfo{
			Scopes: resp.JSON200.Token.Scopes,
			// The account identity, so SDK session-consistency checks have a
			// stable per-user handle. Never the token.
			UserID: resp.JSON200.User.Email,
		}
		if audPresent {
			// The aud key's presence is the /v1/me shape discriminator this
			// verifier already relies on (see the zoa_ canary above): only an
			// OAuth 2.1 grant answers with it. Recording that on TokenInfo
			// lets the insufficient_scope gate reserve its RFC 6750 403 for
			// bearers that can actually run a step-up flow; a PAT holder has
			// no Authorize flow to trigger. The flag is shape-derived
			// configuration, never credential material.
			info.Extra = map[string]any{oauthGrantExtraKey: true}
		}
		if exp := resp.JSON200.Token.ExpiresAt; exp != nil {
			// A real expiry rides along so RequireBearerToken enforces it on
			// every request, including cache hits inside the TTL.
			info.Expiration = *exp
		}
		return info, nil
	case resp.StatusCode() == http.StatusUnauthorized || resp.StatusCode() == http.StatusForbidden:
		// Covers every credential /v1/me refuses: expired/revoked/unknown
		// bearers and zor_ refresh tokens, which the API never accepts as
		// authentication.
		return nil, fmt.Errorf("%w: bearer token rejected by the Melange API", auth.ErrInvalidToken)
	default:
		return nil, fmt.Errorf("token validation failed: /v1/me returned status %d", resp.StatusCode())
	}
}

// meAudience extracts the OAuth audience from a raw 200 /v1/me body.
//
// The generated client predates the OAuth enrichment, so its MeResponse parse
// (which this rides alongside) drops the extra field; the shapes are
// otherwise identical. Per the backend contract (zetic/public/me.py):
// MeOAuthResponse is MeResponse plus token.aud — present (string or null) for
// every zoa_ bearer, never present for a ztp_ PAT. present distinguishes the
// two shapes (the caller's zoa_ canary depends on it); aud == "" with
// present == true means the grant named no resource (aud null). An empty or
// null aud is accepted deliberately, exactly like a PAT's absent key —
// audience enforcement narrows tokens that WERE bound to a resource; it does
// not exclude credential kinds that carry no binding, or every PAT user on
// the HTTP transport would break.
func meAudience(body []byte) (aud string, present bool, err error) {
	var probe struct {
		Token struct {
			Aud json.RawMessage `json:"aud"`
		} `json:"token"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", false, fmt.Errorf("parsing /v1/me token audience: %w", err)
	}
	raw := probe.Token.Aud
	if raw == nil {
		return "", false, nil // key absent: the PAT shape
	}
	if string(raw) == "null" {
		return "", true, nil // key present, grant named no resource
	}
	if err := json.Unmarshal(raw, &aud); err != nil {
		return "", true, fmt.Errorf("parsing /v1/me token audience: %w", err)
	}
	return aud, true, nil
}

// resourceMatches reports whether aud names this server's canonical resource.
// canonical is already in CanonicalResource form; aud (minted by the
// authorization server from its exact-string resource allowlist) gets the
// same trailing-slash trim so `https://mcp.zetic.ai/` and
// `https://mcp.zetic.ai` — one origin in every client's eyes — cannot split.
// Beyond that the comparison is an exact, CASE-SENSITIVE string match, per
// RFC 8707's treat-resources-as-opaque-strings posture. CanonicalResource
// lowercases the configured side's scheme and host, so the authorization
// server's resource allowlist (ZETIC_OAUTH_ALLOWED_RESOURCES) must keep its
// entries lowercase — an uppercase allowlist entry would mint auds this
// comparison rejects.
func resourceMatches(canonical, aud string) bool {
	return strings.TrimSuffix(aud, "/") == canonical
}

// CanonicalResource validates and normalizes a canonical resource URL — the
// identity this server asserts as an OAuth 2.1 protected resource. The same
// value is the audience MeVerifier enforces and the `resource` field of the
// RFC 9728 protected-resource metadata document, whose grammar this enforces:
// an absolute https URL with a host and no query or fragment. Plain http is
// allowed only for loopback hosts (localhost/127.0.0.1/[::1]) so a local dev
// loop does not need TLS; anything else non-https would advertise an identity
// tokens should never be bound to.
//
// Normalization: scheme and host are lowercased, the path is cleaned
// (path.Clean: duplicate slashes collapse, dot segments resolve) and the
// trailing "/" is trimmed, so equivalent spellings configure the same
// identity. Without the clean, `https://host//` canonicalized to
// `https://host/` — an identity no minted aud could ever match (the
// authorization server's allowlist entries have no trailing slash and
// resourceMatches only forgives ONE), and one whose derived RFC 9728
// well-known path ended in "/", a ServeMux SUBTREE pattern that served the
// metadata document at every subpath. Errors are operator-facing startup text
// (the flag value never contains credentials); callers map them to a usage
// error (exit 2).
func CanonicalResource(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("resource URL is empty")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("resource URL %q is not a valid URL", trimmed)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "", fmt.Errorf("resource URL %q must use https (http is allowed only for localhost development)", trimmed)
		}
	default:
		return "", fmt.Errorf("resource URL %q must be an absolute https URL", trimmed)
	}
	if u.Host == "" {
		return "", fmt.Errorf("resource URL %q has no host", trimmed)
	}
	if u.User != nil {
		return "", fmt.Errorf("resource URL %q must not carry userinfo", trimmed)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "" {
		// RFC 9728 §2: a protected-resource identifier has no query or
		// fragment component.
		return "", fmt.Errorf("resource URL %q must not have a query or fragment", trimmed)
	}
	if u.Path != "" {
		// One normalization fixes both `//` symptoms above. Clean works on the
		// decoded path (RawPath is dropped): a resource identifier is operator
		// configuration, not data, so percent-encoded slashes have no business
		// in one.
		u.RawPath = ""
		if cleaned := path.Clean(u.Path); cleaned == "/" || cleaned == "." {
			u.Path = ""
		} else {
			u.Path = cleaned
		}
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

// isLoopbackHost reports whether host names the local machine: the literal
// "localhost" or a loopback IP (127.0.0.0/8, ::1).
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
