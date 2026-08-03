// Package mcp implements the `melange mcp` command, which serves the Melange
// MCP server to agent clients over stdio or Streamable HTTP.
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
	"github.com/zetic-ai/melange-cli/internal/config"
	mcpserver "github.com/zetic-ai/melange-cli/internal/mcp"
	"github.com/zetic-ai/melange-cli/internal/mcp/httpserver"
)

// httpOnlyFlags are the flags that only mean something with --transport http.
// Passing one with stdio is a usage error rather than a silently ignored
// setting: an operator who typed --listen believes something is listening.
var httpOnlyFlags = []string{"listen", "validate-tokens", "allowed-origins"}

// NewCmdMCP returns the `melange mcp` command.
func NewCmdMCP(f *cmdutil.Factory) *cobra.Command {
	var (
		transport      string
		listen         string
		validateTokens bool
		allowedOrigins []string
	)

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the Melange MCP server",
		Long: `Serve the Melange MCP (Model Context Protocol) server so agent clients
can call Melange tools directly.

stdio (the default) speaks MCP on stdin/stdout: stdout carries protocol
frames only, and diagnostics go to stderr. Credentials are resolved lazily
on the first tool call (MELANGE_API_KEY or melange auth login), so the
server starts even when logged out and reports authentication errors per
tool call instead of exiting.

http serves the MCP Streamable HTTP transport on --listen for remote agent
clients. Every request must carry its own credential as
"Authorization: Bearer <personal access token>": the server itself has no
credentials and never reads the local keyring or MELANGE_API_KEY, so one
deployment serves many callers without sharing a token. Requests are
stateless (no session ids), GET /healthz is an unauthenticated liveness
probe, and browser Origins are rejected unless listed in --allowed-origins.
Only API-backed tools are served: anything that would touch the caller's own
machine (model uploads) stays stdio-only, because the server cannot see the
caller's files.

Exit codes: 0 clean disconnect (stdio) or completed drain after SIGINT
(http), 1 serve failure such as an address already in use, 2 usage error,
130 interrupted (stdio).`,
		Example: `  # Register with Claude Code
  claude mcp add melange -- melange mcp

  # Serve on stdio (the default transport)
  melange mcp

  # Serve remote agent clients over HTTP; each request brings its own token
  melange mcp --transport http --listen 0.0.0.0:8080`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch transport {
			case "stdio":
				if err := rejectHTTPOnlyFlags(cmd); err != nil {
					return err
				}
				return runStdio(cmd, f)
			case "http":
				return runHTTP(cmd, f, httpConfig{
					listen:         listen,
					validateTokens: validateTokens,
					allowedOrigins: allowedOrigins,
				})
			default:
				return cmdutil.FlagError{Err: fmt.Errorf(
					"invalid --transport %q: expected \"stdio\" or \"http\"", transport)}
			}
		},
	}

	cmd.Flags().StringVar(&transport, "transport", "stdio",
		`Transport to serve on: "stdio" or "http"`)
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8080",
		"Address to listen on with --transport http (use 0.0.0.0:PORT in a container)")
	cmd.Flags().BoolVar(&validateTokens, "validate-tokens", false,
		"With --transport http, verify each bearer token against the API before serving it")
	cmd.Flags().StringSliceVar(&allowedOrigins, "allowed-origins", nil,
		"With --transport http, browser Origins allowed to call the server (empty rejects all)")

	return cmd
}

// rejectHTTPOnlyFlags fails when an http-only flag was set for stdio.
func rejectHTTPOnlyFlags(cmd *cobra.Command) error {
	for _, name := range httpOnlyFlags {
		if cmd.Flags().Changed(name) {
			return cmdutil.FlagError{Err: fmt.Errorf(
				"--%s requires --transport http", name)}
		}
	}
	return nil
}

// runStdio serves MCP on the process's own stdin/stdout.
func runStdio(cmd *cobra.Command, f *cmdutil.Factory) error {
	// stdio runs on the caller's machine, so local-only tools are in scope.
	srv := mcpserver.New(newDeps(f), mcpserver.Options{EnableLocalTools: true})

	err := srv.Run(cmd.Context(), &mcpsdk.StdioTransport{})
	if isCleanDisconnect(err) {
		return nil
	}
	// SIGINT propagates as context.Canceled (exit 130 via ExitCode).
	return err
}

// httpConfig carries the http-only flag values into runHTTP.
type httpConfig struct {
	listen         string
	validateTokens bool
	allowedOrigins []string
}

// runHTTP serves the Streamable HTTP transport until the command's context is
// canceled.
//
// Note what is deliberately absent: any local credential lookup. stdio may
// resolve a token from the keyring or MELANGE_API_KEY because it serves
// exactly one caller — the user who started it. An HTTP deployment serves
// many, so the only credential that may ever be used is the one on the
// request (httpserver binds a fresh API client to it per request).
// f.ApiClient is never called on this path, and TestHTTPModeNeverUsesLocalCredentials
// pins that. The host, by contrast, IS a local setting: it says which API this
// deployment fronts, so it follows the usual --host/MELANGE_HOST/config
// precedence.
func runHTTP(cmd *cobra.Command, f *cmdutil.Factory, hc httpConfig) error {
	host, err := resolveHost(f)
	if err != nil {
		return err
	}
	timeout, err := cmdutil.APITimeout()
	if err != nil {
		return err
	}

	srv, err := httpserver.New(httpserver.Config{
		Listen:         hc.listen,
		APIHost:        host,
		UserAgent:      cmdutil.UserAgent(f.Version),
		APITimeout:     timeout,
		Version:        build.Version,
		Logger:         serverLogger(f, slog.LevelInfo),
		AllowedOrigins: hc.allowedOrigins,
		ValidateTokens: hc.validateTokens,
	})
	if err != nil {
		return err
	}

	// SIGINT/SIGTERM cancels cmd.Context(); ListenAndServe treats that as the
	// operator's stop signal, drains in-flight requests, and returns nil — so
	// an interrupted HTTP server exits 0, where an interrupted stdio server
	// exits 130.
	//
	// The divergence is deliberate. 130 means "the user interrupted work that
	// did not finish"; it is the right answer for a stdio session abandoned
	// mid-conversation. A long-running server has no such work: stopping IS
	// the requested operation, and every process supervisor that will run this
	// (systemd, ECS, Kubernetes) reads a nonzero status on an orderly stop as
	// a crash and reports it as one. A drain that overruns its deadline does
	// abandon work, and ListenAndServe returns an error there (exit 1).
	return srv.ListenAndServe(cmd.Context())
}

// resolveHost returns the API host this deployment fronts, following the
// standard precedence (--host > MELANGE_HOST > config > default).
func resolveHost(f *cmdutil.Factory) (string, error) {
	var cfg *config.Config
	if f.Config != nil {
		var err error
		if cfg, err = f.Config(); err != nil {
			return "", err
		}
	}
	// ResolveHost is nil-safe, so a factory with no config loader still gets
	// flag/env/default precedence rather than a panic.
	return cfg.ResolveHost(f.HostOverride).Value, nil
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

// stderrLogger builds the stdio server's diagnostic logger. It writes to
// stderr and never to stdout, which carries protocol frames exclusively. The
// default level is warn so a normal session stays quiet in the client's log
// pane; MELANGE_DEBUG lowers it to debug, matching the API client's debug
// switch.
func stderrLogger(f *cmdutil.Factory) *slog.Logger {
	return serverLogger(f, slog.LevelWarn)
}

// serverLogger builds a stderr logger at the given floor, or debug when
// MELANGE_DEBUG is set. The floors differ by transport on purpose: stdio's
// lines land in an agent client's log pane, where anything below a warning is
// noise, while an HTTP deployment is a daemon whose operator needs its
// listening and draining lines in the container log.
func serverLogger(f *cmdutil.Factory, level slog.Level) *slog.Logger {
	if cmdutil.DebugEnabled() {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(f.IOStreams.ErrOut, &slog.HandlerOptions{Level: level}))
}
