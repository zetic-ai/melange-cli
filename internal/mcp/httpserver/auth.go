package httpserver

import (
	"context"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// This file is the auth assembly point for the MCP endpoint. Task 1 wires a
// temporary always-allow verifier so the chain and the per-request
// ClientProvider are real end-to-end; CLI-PR2 Task 2 replaces the verifier
// with PassthroughVerifier (any non-empty bearer) and the optional
// --validate-tokens MeVerifier. Only the verifier changes; the assembly stays.

// AuthMiddleware wraps the MCP handler with bearer authentication: the SDK's
// RequireBearerToken rejects requests without a well-formed bearer (401) and
// runs verifier on the token, then bearerToContext captures the raw bearer in
// the request context for the per-request ClientProvider. /healthz is never
// behind this middleware.
func AuthMiddleware(verifier auth.TokenVerifier, next http.Handler) http.Handler {
	require := auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
		// Melange personal access tokens carry no in-band expiry; their
		// lifetime is managed server-side. Without this, the middleware would
		// 401 every TokenInfo whose Expiration is zero.
		AllowMissingExpiration: true,
	})
	return require(bearerToContext(next))
}

// tempAllowAllVerifier accepts every bearer token without validating it.
//
// TEMPORARY (CLI-PR2 Task 1): it exists only so the middleware chain can be
// wired and tested before the real verifiers land in Task 2. It never
// inspects, stores, or logs the token. RequireBearerToken has already
// guaranteed a non-empty bearer before the verifier runs.
func tempAllowAllVerifier(context.Context, string, *http.Request) (*auth.TokenInfo, error) {
	return &auth.TokenInfo{}, nil
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
