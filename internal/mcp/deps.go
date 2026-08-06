package mcp

import (
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Deps carries the shared dependencies every MCP tool handler receives.
type Deps struct {
	// Clients supplies the generated API client used by tool handlers.
	Clients ClientProvider
	// Version is the server version advertised during initialization.
	Version string
	// Logger receives server diagnostics; nil means discard.
	Logger *slog.Logger
	// AuthHints overrides the remediation text attached to credential
	// failures. The zero value keeps the stdio defaults.
	AuthHints AuthHints
	// Bare is the HTTP client the local upload tool uses against signed
	// storage URLs. Like the CLI's bare client, it must carry NO API
	// transport chain — no Authorization header, no debug logging — because
	// signed URLs and resumable session URIs are credentials. Nil selects a
	// plain default-transport client; tests inject their mock transport.
	Bare *http.Client
}

// AuthHints carries transport-specific remediation text for credential
// failures in tool errors. The stdio transport tells the caller to run
// 'melange auth login' on their own machine; an HTTP transport serves remote
// clients for whom that advice is wrong, so it supplies its own text. An
// empty field falls back to the stdio default, keeping existing stdio
// behavior byte-identical.
type AuthHints struct {
	// Unauthenticated is appended to authentication failures: a missing
	// credential or an API authentication_error (HTTP 401).
	Unauthenticated string
	// Forbidden is appended to permission failures: an API permission_error
	// (HTTP 403), typically missing token scopes.
	Forbidden string
}

// Options configures which parts of the tool catalog a server exposes.
type Options struct {
	// EnableLocalTools exposes tools that only make sense when the server
	// runs on the same machine as the caller (e.g. file uploads over stdio).
	// False hides them, as an HTTP transport must.
	EnableLocalTools bool
	// SchemaCache, when non-nil, lets the SDK reuse resolved tool schemas
	// across every server built with the same cache. The HTTP transport
	// builds one server per request for token isolation and passes one
	// process-wide cache so schema resolution is paid once, not per request;
	// combined with this package's memoized schema pointers (the cache keys
	// provided schemas by pointer identity) that removes almost all of the
	// per-request construction cost. Nil — stdio's default — keeps the SDK
	// resolving per server, exactly as before. It lives on Options, not Deps,
	// because it shapes construction and is never seen by a tool handler.
	SchemaCache *mcp.SchemaCache
}

// logger returns the configured logger, or a discard logger when nil, so
// callers never need a nil check.
func (d Deps) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// bareClient returns the configured bare storage client, or a plain client
// over the default transport, so callers never need a nil check.
func (d Deps) bareClient() *http.Client {
	if d.Bare != nil {
		return d.Bare
	}
	return &http.Client{}
}
