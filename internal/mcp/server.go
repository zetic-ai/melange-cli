// Package mcp implements the Melange MCP server: a tool catalog over the
// generated API client, transport-agnostic so the stdio command (and a later
// HTTP transport) only wire dependencies. Tool success payloads carry the API
// response bytes untouched; API failures surface as IsError tool results.
package mcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New builds the MCP server with the full tool catalog registered.
// opts.EnableLocalTools additionally registers the tools that only make sense
// when the server runs on the caller's machine — today upload_model, which
// reads local files; the HTTP transport keeps it hidden.
func New(deps Deps, opts Options) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: deps.Edition.ProgramName(), Version: deps.Version},
		// quietSDKLogger drops the SDK's three content-free per-connection
		// INFO lines for both transports; see lognoise.go.
		&mcp.ServerOptions{Logger: quietSDKLogger(deps.logger()), SchemaCache: opts.SchemaCache},
	)
	registerAccount(s, deps)
	registerRepo(s, deps)
	registerModel(s, deps)
	registerDeploy(s, deps)
	registerReport(s, deps)
	registerLibrary(s, deps)
	registerDownload(s, deps)
	if opts.EnableLocalTools {
		registerUpload(s, deps)
	}
	return s
}

// falsePtr returns a pointer to false, for annotation hints whose default is
// true (DestructiveHint, OpenWorldHint) and must therefore be set explicitly.
func falsePtr() *bool {
	f := false
	return &f
}

// truePtr returns a pointer to true, for annotation hints that are pointers
// and must be set explicitly (e.g. OpenWorldHint on a tool that reaches
// third-party systems).
func truePtr() *bool {
	t := true
	return &t
}
