// Package mcp implements the `melange mcp` command, which serves the Melange
// MCP server to agent clients over stdio.
package mcp

import (
	"errors"
	"fmt"
	"io"
	"log/slog"

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
			if err == nil || errors.Is(err, io.EOF) {
				// Clean client disconnect.
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
