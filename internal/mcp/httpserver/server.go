// Package httpserver serves the Melange MCP server over the MCP Streamable
// HTTP transport for remote agent clients.
//
// The design is stateless by construction: every request builds a fresh
// *mcp.Server whose API client is bound to that request's verified bearer
// token, so N replicas behind a load balancer share nothing and no request
// can ever observe another request's credential. The bearer token itself
// never appears in logs, error text, or tool results.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	mcpserver "github.com/zetic-ai/melange-cli/internal/mcp"
)

const (
	// readHeaderTimeout bounds slow-header (Slowloris) clients.
	readHeaderTimeout = 10 * time.Second
	// idleTimeout reclaims keep-alive connections that go quiet.
	idleTimeout = 120 * time.Second
	// drainTimeout is how long Shutdown waits for in-flight requests before
	// the server is force-closed and ListenAndServe returns an error (exit 1).
	drainTimeout = 25 * time.Second

	// maxRequestBodyBytes caps request bodies read by the streamable handler;
	// the SDK rejects overruns with 413 during the read (MaxBytesReader), so
	// an oversized POST can never balloon memory. 4 MiB restates the SDK's
	// own default (mcp.DefaultMaxRequestBodyBytes) as an explicit, reviewed
	// choice: large enough for any real tool call, small enough that a
	// request flood is bounded by connection count, not body size.
	maxRequestBodyBytes = 4 << 20
)

// httpAuthHints replaces the stdio remediation text in tool errors: a remote
// MCP client cannot run 'melange auth login' on the server, so credential
// failures must point at the token the client connected with.
var httpAuthHints = mcpserver.AuthHints{
	Unauthenticated: "The Authorization bearer token was rejected. Verify the " +
		"personal access token (create one at https://zetic.ai settings) and reconnect.",
	Forbidden: "The Authorization bearer token lacks the required scopes. Create a " +
		"personal access token with the needed scopes at https://zetic.ai settings and reconnect.",
}

// Config configures a Server. Listen and APIHost are required.
type Config struct {
	// Listen is the TCP listen address, e.g. ":8321" or "127.0.0.1:0".
	Listen string
	// APIHost is the Melange API base URL for per-request clients.
	APIHost string
	// UserAgent is sent on outgoing API requests.
	UserAgent string
	// APITimeout bounds one outgoing API request; 0 means
	// api.DefaultRequestTimeout.
	APITimeout time.Duration
	// Version is the MCP server version advertised during initialization and
	// reported by /healthz.
	Version string
	// Logger receives server diagnostics; nil means discard. It must never
	// receive credentials.
	Logger *slog.Logger
	// AllowedOrigins lists the browser Origins allowed to reach the MCP
	// endpoint. Empty rejects every request that carries an Origin header;
	// see originMiddleware for the rationale.
	AllowedOrigins []string
	// ValidateTokens verifies bearers against GET /v1/me before serving
	// (CLI-PR2 Task 2). Until that verifier lands, New rejects true rather
	// than silently serving unvalidated tokens.
	ValidateTokens bool
}

// Server is the Streamable HTTP front end for the Melange MCP server.
type Server struct {
	cfg        Config
	logger     *slog.Logger
	handler    http.Handler
	httpServer *http.Server

	mu   sync.Mutex
	addr net.Addr
}

// New validates cfg and assembles the server. The handler chain for the MCP
// endpoint is (outermost first): Origin policy -> bearer auth -> bearer
// capture -> streamable handler; /healthz bypasses all of it.
func New(cfg Config) (*Server, error) {
	if cfg.Listen == "" {
		return nil, errors.New("httpserver: Listen address is required")
	}
	if cfg.APIHost == "" {
		return nil, errors.New("httpserver: APIHost is required")
	}
	if cfg.ValidateTokens {
		// Fail-fast guard: the MeVerifier lands in CLI-PR2 Task 2. Until it
		// does, accepting this flag would hand the operator an unvalidated
		// relay while they believe tokens are checked. Task 2 deletes this
		// guard when it wires the real verifier.
		return nil, errors.New("httpserver: token validation (ValidateTokens) is not implemented yet")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Server{cfg: cfg, logger: logger}

	mcpHandler := sdk.NewStreamableHTTPHandler(s.getServer, &sdk.StreamableHTTPOptions{
		// Stateless: no Mcp-Session-Id is issued or required, every POST is
		// self-contained, and server->client requests (sampling/elicitation)
		// are rejected — exactly the multi-replica ALB posture. GET/DELETE
		// return 405.
		Stateless: true,
		// Plain application/json responses; no SSE streaming to keep open.
		JSONResponse:        true,
		MaxRequestBodyBytes: maxRequestBodyBytes,
		Logger:              logger,
	})

	// Task 2 swaps tempAllowAllVerifier for the real verifiers (and consults
	// cfg.ValidateTokens); the chain shape is fixed here.
	protected := originMiddleware(cfg.AllowedOrigins, AuthMiddleware(tempAllowAllVerifier, mcpHandler))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("/", protected)
	s.handler = mux

	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		// Route net/http's own diagnostics (TLS handshake noise, panics)
		// through the structured logger instead of the process stderr.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	return s, nil
}

// ListenAndServe binds cfg.Listen and serves until ctx is canceled, then
// drains: in-flight requests get drainTimeout to complete, after which the
// server returns nil (exit 0). A drain overrun force-closes remaining
// connections and returns an error (exit 1).
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("binding %s: %w", s.cfg.Listen, err)
	}
	s.mu.Lock()
	s.addr = ln.Addr()
	s.mu.Unlock()
	s.logger.Info("mcp http server listening", "addr", ln.Addr().String())

	serveErr := make(chan error, 1)
	go func() { serveErr <- s.httpServer.Serve(ln) }()

	select {
	case err := <-serveErr:
		// Serve only fails on its own for listener/accept faults; Shutdown
		// has not been called, so ErrServerClosed cannot arrive here.
		return err
	case <-ctx.Done():
	}

	s.logger.Info("mcp http server draining", "timeout", drainTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		s.httpServer.Close()
		return fmt.Errorf("drain deadline (%s) exceeded, connections force-closed: %w", drainTimeout, err)
	}
	<-serveErr // Serve has returned http.ErrServerClosed.
	return nil
}

// Addr reports the bound listen address, or nil before ListenAndServe binds
// it. With a ":0" listen address this is the only way to learn the port.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// getServer builds the per-request MCP server: a fresh tool catalog whose
// ClientProvider is bound to this request's bearer, with HTTP remediation
// hints and no local-only tools.
func (s *Server) getServer(req *http.Request) *sdk.Server {
	bearer, ok := bearerFromRequest(req)
	if !ok {
		// Unreachable behind AuthMiddleware; returning nil makes the SDK
		// answer 400 instead of serving a session with no credential.
		s.logger.Error("mcp request reached getServer without a bearer token")
		return nil
	}
	return mcpserver.New(mcpserver.Deps{
		Clients:   requestProvider{opts: s.apiOptions(bearer)},
		Version:   s.cfg.Version,
		Logger:    s.logger,
		AuthHints: httpAuthHints,
	}, mcpserver.Options{
		// Local tools act on the server's own filesystem, which is
		// meaningless (and dangerous) for a remote caller.
		EnableLocalTools: false,
	})
}

// apiOptions builds the outgoing API client options for one request's bearer.
//
// Debug is deliberately never set: MELANGE_DEBUG's transport dump writes raw
// request/response bytes — including the Authorization header — to a writer.
// That is fine for a stdio server dumping the caller's own traffic to the
// caller's own stderr, and a credential leak on a shared HTTP server, so HTTP
// mode keeps it off regardless of the environment.
func (s *Server) apiOptions(bearer string) api.Options {
	return api.Options{
		Host:      s.cfg.APIHost,
		Token:     bearer,
		UserAgent: s.cfg.UserAgent,
		Timeout:   s.cfg.APITimeout,
	}
}

// requestProvider resolves a generated API client bound to one request's
// bearer. Unlike StaticProvider there is no cache and no lock: the provider
// lives for a single HTTP request, and constructing the client is a URL parse
// plus transport assembly.
type requestProvider struct {
	opts api.Options
}

// Client implements mcpserver.ClientProvider.
func (p requestProvider) Client(context.Context) (*gen.ClientWithResponses, error) {
	c, err := api.NewClient(p.opts)
	if err != nil {
		return nil, err
	}
	return c.Gen()
}

// healthz serves the unauthenticated liveness probe.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Status  string `json:"status"`
		Version string `json:"version,omitempty"`
	}{Status: "ok", Version: s.cfg.Version})
}

// originMiddleware enforces the browser-origin posture for a public host.
//
// What go-sdk v1.7.0 actually does (mcp/streamable.go, verified in source):
//
//   - The handler's built-in "DNS rebinding protection" is Host-header based
//     and loopback-only: ServeHTTP rejects (403) requests that arrive on a
//     loopback local address with a non-loopback Host. On a public host that
//     check never fires, and it never looks at Origin. We leave it enabled —
//     it is exactly right for local development listeners.
//   - Origin checking is OFF by default: StreamableHTTPOptions's
//     CrossOriginProtection field is nil unless set (the field is deprecated
//     in favor of external middleware, and only the MCPGODEBUG
//     enableoriginverification=1 compatibility switch restores the old
//     default).
//
// So the SDK provides no Origin enforcement for a public deployment, and this
// middleware is our explicit policy: requests without an Origin header are
// non-browser clients (MCP SDKs, curl) and pass; requests with an Origin must
// match the allowlist exactly (case-insensitive), or they are rejected 403.
// The default empty allowlist rejects every browser origin: a
// bearer-authenticated MCP endpoint has no browser use case unless the
// operator opts one in via --allowed-origins, and rejecting by default closes
// browser-driven CSRF/DNS-rebinding abuse.
func originMiddleware(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		for _, a := range allowed {
			if strings.EqualFold(origin, a) {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "Forbidden: browser origin not allowed", http.StatusForbidden)
	})
}
