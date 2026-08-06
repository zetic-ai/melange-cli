package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// This file publishes the server's OAuth 2.0 protected-resource metadata
// (RFC 9728) — the discovery document an OAuth-capable MCP client fetches
// after a 401 to learn which authorization server can mint a token for this
// resource. It exists only when the operator configured a resource identity
// (--resource): an RFC 9728 document is a statement about a resource
// identifier, and a server with no identity has nothing truthful to publish,
// so New registers no route and 401 challenges stay bare (see server.go).

// protectedResourceWellKnown is the RFC 9728 §3 well-known path segment under
// which a protected resource's metadata is published.
const protectedResourceWellKnown = "/.well-known/oauth-protected-resource"

// metadataCacheControl allows shared caches to hold the discovery document
// briefly. The document is a pure function of startup config, so it changes
// only on redeploy; one hour bounds how long a stale resource identity or API
// host can outlive one.
const metadataCacheControl = "public, max-age=3600"

// resourceScopes is every scope a client may request for this resource,
// mirroring the authorization server's own scopes_supported (["read","write"]
// per the RFC 8414 document the backend serves). The two lists are one
// contract: a scope advertised here must be one the AS can grant.
var resourceScopes = []string{"read", "write"}

// protectedResourceMetadataLocation derives where resource's RFC 9728
// metadata lives: the path this server must serve it on, and the absolute URL
// advertised as the challenge's resource_metadata parameter. Per RFC 9728 §3
// the well-known segment is inserted between host and the resource
// identifier's path component, so "https://mcp.zetic.ai" maps to
// /.well-known/oauth-protected-resource while "https://mcp.zetic.ai/mcp" maps
// to /.well-known/oauth-protected-resource/mcp.
//
// resource is already in CanonicalResource form (New re-normalizes before
// calling), so the parse cannot fail in practice; the error path is defense
// for a future caller passing raw input.
func protectedResourceMetadataLocation(resource string) (path, absoluteURL string, err error) {
	u, err := url.Parse(resource)
	if err != nil {
		return "", "", fmt.Errorf("parsing canonical resource URL: %w", err)
	}
	path = protectedResourceWellKnown + u.EscapedPath()
	return path, u.Scheme + "://" + u.Host + path, nil
}

// protectedResourceMetadataHandler serves the RFC 9728 document for resource
// (canonical form), naming apiHost as the sole authorization server.
//
// The document itself comes from the SDK's ProtectedResourceMetadataHandler
// over the SDK's RFC-typed struct, which supplies the pieces discovery
// clients depend on: application/json content type, GET/OPTIONS method
// dispatch, and Access-Control-Allow-Origin: * — the metadata is public
// configuration (RFC 9728 §3.1), and a browser-based agent must be able to
// read it cross-origin before it holds any credential. This wrapper adds only
// the cache policy, and only on GET: a Cache-Control on the 405/preflight
// answers would make those responses cacheable too (RFC 9111 §3 caches any
// response with an explicit freshness lifetime).
//
// Every value derives from config — no hardcoded hostnames. authorization_
// servers carries the API host in RFC 8414 issuer form (no trailing slash),
// so appending /.well-known/oauth-authorization-server yields the AS's own
// metadata URL and the discovery chain stays coherent end to end.
func protectedResourceMetadataHandler(resource, apiHost string) http.Handler {
	inner := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               resource,
		AuthorizationServers:   []string{strings.TrimSuffix(apiHost, "/")},
		ScopesSupported:        resourceScopes,
		BearerMethodsSupported: []string{"header"},
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Cache-Control", metadataCacheControl)
		}
		inner.ServeHTTP(w, r)
	})
}
