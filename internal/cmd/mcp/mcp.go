// Package mcp implements the `melange mcp` command, which serves the Melange
// MCP server to agent clients over stdio or Streamable HTTP.
package mcp

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

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
var httpOnlyFlags = []string{"listen", "validate-tokens", "allowed-origins", "resource"}

// NewCmdMCP returns the `melange mcp` command.
func NewCmdMCP(f *cmdutil.Factory) *cobra.Command {
	var (
		transport      string
		listen         string
		validateTokens bool
		allowedOrigins []string
		resource       string
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
clients. Every request must carry its own credential — a personal access
token or an OAuth access token — as "Authorization: Bearer <token>": the
server itself has no credentials and never reads the local keyring or
MELANGE_API_KEY, so one deployment serves many callers without sharing a
token. Requests are stateless (no session ids), GET /healthz is an
unauthenticated liveness probe, and browser Origins are rejected unless
listed in --allowed-origins. Setting --resource (or MELANGE_MCP_RESOURCE)
declares the server's canonical URL as an OAuth protected resource: every
bearer is then validated against the API, and OAuth tokens bound to a
different resource are rejected. Only API-backed tools are served: anything
that would touch the caller's own machine (model uploads) stays stdio-only,
because the server cannot see the caller's files.

Exit codes: 0 clean disconnect (stdio) or completed drain after SIGINT or
SIGTERM (http), 1 serve failure such as an address already in use or a drain
that overran its deadline, 2 usage error, 130 interrupted (stdio).`,
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
					resource:       resource,
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
	cmd.Flags().StringVar(&resource, "resource", "",
		"With --transport http, this server's canonical resource URL as an OAuth protected "+
			"resource (also read from MELANGE_MCP_RESOURCE); implies token validation and "+
			"rejects OAuth tokens bound to a different resource")

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
	resource       string
}

// resolveResource resolves the canonical resource URL: --resource wins, else
// MELANGE_MCP_RESOURCE, else "" (no resource identity, no audience
// enforcement). A set-but-invalid value from either source is a usage error
// (exit 2), never a silent fallback: an operator who named a resource
// identity must not run a server that enforces a different one — or none.
func resolveResource(cmd *cobra.Command, flagValue string) (string, error) {
	raw, source := flagValue, "--resource"
	if !cmd.Flags().Changed("resource") {
		raw, source = strings.TrimSpace(os.Getenv("MELANGE_MCP_RESOURCE")), "MELANGE_MCP_RESOURCE"
	}
	if raw == "" {
		return "", nil
	}
	resource, err := httpserver.CanonicalResource(raw)
	if err != nil {
		return "", cmdutil.FlagError{Err: fmt.Errorf("invalid %s: %w", source, err)}
	}
	return resource, nil
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
	resource, err := resolveResource(cmd, hc.resource)
	if err != nil {
		return err
	}
	if resource != "" && cmd.Flags().Changed("validate-tokens") && !hc.validateTokens {
		// A resource identity means every bearer must be validated (audience
		// enforcement happens inside validation), so an explicit
		// --validate-tokens=false contradicts it. Refusing beats silently
		// validating against the operator's stated wish — or silently
		// dropping the enforcement they configured.
		return cmdutil.FlagError{Err: errors.New(
			"--resource requires token validation; drop --validate-tokens=false")}
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
		Resource:       resource,
	})
	if err != nil {
		return err
	}

	// A canceled context is the operator's stop signal: ListenAndServe drains
	// in-flight requests and returns nil, so a signaled HTTP server exits 0
	// where a signaled stdio server exits 130.
	//
	// The divergence is deliberate. 130 means "the user interrupted work that
	// did not finish"; it is the right answer for a stdio session abandoned
	// mid-conversation. A long-running server has no such work: stopping IS
	// the requested operation, and every process supervisor that will run this
	// (systemd, ECS, Kubernetes) reads a nonzero status on an orderly stop as
	// a crash and reports it as one. A drain that overruns its deadline does
	// abandon work, and ListenAndServe returns an error there (exit 1).
	//
	// Both stop signals have to reach that drain. SIGINT is handled
	// process-wide (cmd/melange/main.go); SIGTERM is added here, derived from
	// cmd.Context() so SIGINT still propagates, and deliberately not added
	// there. Those same supervisors stop a process by sending SIGTERM,
	// waiting, then SIGKILL, so a drain wired only to SIGINT is a drain that
	// never runs in production — but for a one-shot command SIGTERM's default
	// action, terminate now, is the right answer. Making it cancelable
	// process-wide would let any command that does not thread its context
	// through ignore `kill` entirely, turning a reliable stop into a hang. A
	// server is the case where the signal must be a request rather than a
	// kill, because there is in-flight work to finish.
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM)
	defer stop()
	return srv.ListenAndServe(ctx)
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
