package mcp

import "log/slog"

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
}

// logger returns the configured logger, or a discard logger when nil, so
// callers never need a nil check.
func (d Deps) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.New(slog.DiscardHandler)
}
