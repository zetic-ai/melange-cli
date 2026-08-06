// Package mcp implements the `melange mcp` command, which serves the Melange
// MCP server to agent clients over stdio.
package mcp

import (
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/build"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	mcpserver "github.com/zetic-ai/melange-cli/internal/mcp"
)

// NewCmdMCP returns the `melange mcp` command.
func NewCmdMCP(f *cmdutil.Factory) *cobra.Command {
	var transport string

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the Melange MCP server",
		Long: `Serve the Melange MCP (Model Context Protocol) server so agent clients
can call Melange tools directly.

The process speaks MCP on stdin/stdout: stdout carries protocol frames
only, and diagnostics go to stderr. Credentials are resolved lazily on the
first tool call (MELANGE_API_KEY or melange auth login), so the server
starts even when logged out and reports authentication errors per tool
call instead of exiting.

--transport currently accepts only "stdio"; "http" arrives later.

Exit codes: 0 clean disconnect, 2 usage error, 130 interrupted.`,
		Example: `  # Register with Claude Code
  claude mcp add melange -- melange mcp

  # Serve on stdio (the default transport)
  melange mcp`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if transport != "stdio" {
				return cmdutil.FlagError{Err: fmt.Errorf(
					"invalid --transport %q: only \"stdio\" is supported (\"http\" arrives later)", transport)}
			}

			// stdio runs on the caller's machine, so local-only tools are in scope.
			srv := mcpserver.New(newDeps(f), mcpserver.Options{EnableLocalTools: true})

			err := srv.Run(cmd.Context(), &mcpsdk.StdioTransport{})
			if isCleanDisconnect(err) {
				return nil
			}
			// SIGINT propagates as context.Canceled (exit 130 via ExitCode).
			return err
		},
	}

	cmd.Flags().StringVar(&transport, "transport", "stdio",
		`Transport to serve on: "stdio" (later: "http")`)

	return cmd
}

// codeServerClosing is the JSON-RPC error code the SDK's jsonrpc2 layer
// attaches to work abandoned because the connection is shutting down
// (`jsonrpc2.ErrServerClosing`). The SDK exports the jsonrpc.Error type but not
// that sentinel, so only the wire code can be named here.
//
// Restating an SDK-internal value is a liability, so it is not left to pin
// itself: TestIsCleanDisconnectAcceptsRealHangup drives a real go-sdk server
// through a client hangup and classifies the error the SDK actually returns, so
// an upgrade that changes this code or shape fails the build rather than
// silently restoring the exit-1 regression.
const codeServerClosing = -32004

// isCleanDisconnect reports whether a server Run error means "stop serving,
// without fault" — a successful end of service (exit 0) rather than a failure.
//
// Two shapes qualify. io.EOF is stdin running out. ErrServerClosing is any
// reply that could not be written once shutdown had already begun; in practice
// that is the peer hanging up mid-request (a client closing stdin during a tool
// call, or a one-shot `printf ... | melange mcp`), though a broken stdout — a
// full disk on a redirected stream, say — reaches it too. Both mean this
// process has no one left to answer, which is not an operator-actionable
// failure. Everything else — protocol faults, context.Canceled from SIGINT —
// keeps its non-zero exit.
func isCleanDisconnect(err error) bool {
	if err == nil || errors.Is(err, io.EOF) {
		return true
	}
	// WireError.Is compares codes, so a code-only target matches by code.
	return errors.Is(err, &jsonrpc.Error{Code: codeServerClosing})
}

// newDeps assembles the server dependencies from the CLI factory: a lazily
// resolved API client (so the server starts logged out) and a stderr logger.
func newDeps(f *cmdutil.Factory) mcpserver.Deps {
	return mcpserver.Deps{
		Clients: mcpserver.NewStaticProvider(func() (*gen.ClientWithResponses, error) {
			client, err := f.ApiClient()
			if err != nil {
				return nil, err
			}
			return client.Gen()
		}),
		Version: build.Version,
		Logger:  stderrLogger(f),
	}
}

// stderrLogger builds the server's diagnostic logger. It writes to stderr and
// never to stdout, which carries protocol frames exclusively. The default
// level is warn so a normal session stays quiet in the client's log pane;
// MELANGE_DEBUG lowers it to debug, matching the API client's debug switch.
func stderrLogger(f *cmdutil.Factory) *slog.Logger {
	level := slog.LevelWarn
	if cmdutil.DebugEnabled() {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(f.IOStreams.ErrOut, &slog.HandlerOptions{Level: level}))
}
