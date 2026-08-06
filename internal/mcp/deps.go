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
