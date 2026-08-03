// Package mcp implements the Melange MCP server: a tool catalog over the
// generated API client, transport-agnostic so the stdio command (and a later
// HTTP transport) only wire dependencies. Tool success payloads carry the API
// response bytes untouched; API failures surface as IsError tool results.
package mcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName is the implementation name advertised during initialization.
const serverName = "melange"

// New builds the MCP server with the full tool catalog registered.
// opts.EnableLocalTools gates stdio-only tools; none exist yet, so it has no
// effect on the current catalog.
func New(deps Deps, opts Options) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: deps.Version},
		&mcp.ServerOptions{Logger: deps.logger()},
	)
	registerAccount(s, deps)
	registerRepo(s, deps)
	registerModel(s, deps)
	return s
}

// falsePtr returns a pointer to false, for annotation hints whose SDK default
// is true (e.g. DestructiveHint) and must be set explicitly.
func falsePtr() *bool {
	f := false
	return &f
}
