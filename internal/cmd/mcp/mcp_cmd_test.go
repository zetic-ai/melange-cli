package mcp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

func run(t *testing.T, args ...string) (stdout, stderr *bytes.Buffer, err error) {
	t.Helper()
	ios, in, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Version: "test"}
	cmd := root.NewCmdRoot(f)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	return out, errOut, cmd.ExecuteContext(context.Background())
}

func TestMCPBadTransportIsFlagError(t *testing.T) {
	// "http" is a supported transport now, so the invalid set covers the
	// shapes a user actually mistypes: a different protocol, an empty value,
	// and the spec's own name for the HTTP transport.
	for _, transport := range []string{"tcp", "", "streamable-http"} {
		t.Run("transport="+transport, func(t *testing.T) {
			_, _, err := run(t, "mcp", "--transport", transport)
			require.Error(t, err)
			assert.Equal(t, 2, cmdutil.ExitCode(err), "bad --transport must exit 2")
			assert.Contains(t, err.Error(), "--transport")
			assert.Contains(t, err.Error(), "stdio")
			assert.Contains(t, err.Error(), "http")
		})
	}
}

// TestHTTPOnlyFlagsRequireHTTPTransport pins the flag matrix: a flag that only
// configures the HTTP server must be a usage error on stdio, never a silently
// ignored setting. An operator who passed --listen believes a port is open.
func TestHTTPOnlyFlagsRequireHTTPTransport(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"listen", []string{"mcp", "--listen", "127.0.0.1:9"}},
		{"validate-tokens", []string{"mcp", "--validate-tokens"}},
		{"allowed-origins", []string{"mcp", "--allowed-origins", "https://app.zetic.ai"}},
		{"listen with explicit stdio", []string{"mcp", "--transport", "stdio", "--listen", "127.0.0.1:9"}},
		{"validate-tokens with explicit stdio", []string{"mcp", "--transport", "stdio", "--validate-tokens"}},
		{"allowed-origins with explicit stdio", []string{"mcp", "--transport", "stdio", "--allowed-origins", "https://a"}},
		{"several at once", []string{"mcp", "--listen", "127.0.0.1:9", "--validate-tokens"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := run(t, tt.args...)
			require.Error(t, err, "http-only flags must not be accepted on stdio")
			assert.Equal(t, 2, cmdutil.ExitCode(err), "misused flags are usage errors (exit 2)")
			assert.Contains(t, err.Error(), "--transport http",
				"the error must name the transport that would make the flag valid")
		})
	}
}

func TestMCPRejectsPositionalArgs(t *testing.T) {
	_, _, err := run(t, "mcp", "serve")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
}

// TestMCPHelp pins that help documents both transports and the facts a
// remote operator cannot discover by trial and error: how HTTP mode
// authenticates, that a health probe exists, and that machine-local tools are
// stdio-only.
func TestMCPHelp(t *testing.T) {
	stdout, _, err := run(t, "mcp", "--help")
	require.NoError(t, err)
	help := stdout.String()

	for _, want := range []string{
		"--transport", "stdio", "http",
		"--listen", "--validate-tokens", "--allowed-origins",
		"Authorization: Bearer", "/healthz",
	} {
		assert.Contains(t, help, want, "help must document %q", want)
	}
	assert.Contains(t, help, "never reads the local keyring",
		"help must state that HTTP mode ignores machine-local credentials")
	assert.Contains(t, help, "stdio-only",
		"help must state that local-machine tools are not served over HTTP")
}

// logSink is a goroutine-safe stderr: the server logs from the command's
// goroutine while the test reads the address it bound.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

var listenAddrRE = regexp.MustCompile(`addr=(\S+)`)

// serving starts `melange mcp` in the background and returns the base URL it
// bound plus a channel carrying the command's final error. The address comes
// from the server's own startup log line, so --listen can use port 0 and the
// test never races another process for a fixed port.
type serving struct {
	baseURL string
	logs    *logSink
	cancel  context.CancelFunc

	done <-chan error
	once sync.Once
	err  error
}

// wait collects the command's exit error exactly once, so a test that asserts
// on it and the cleanup that guarantees the goroutine ends can both call it.
func (s *serving) wait(t *testing.T) error {
	t.Helper()
	s.once.Do(func() {
		select {
		case s.err = <-s.done:
		case <-time.After(30 * time.Second):
			t.Error("mcp http server did not exit after cancellation")
		}
	})
	return s.err
}

func serveHTTP(t *testing.T, f *cmdutil.Factory, args ...string) *serving {
	t.Helper()
	logs := &logSink{}
	ios, in, out, _ := iostreams.Test()
	ios.ErrOut = logs
	f.IOStreams = ios
	if f.Version == "" {
		f.Version = "test"
	}

	cmd := root.NewCmdRoot(f)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"mcp", "--transport", "http", "--listen", "127.0.0.1:0"}, args...))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	s := &serving{logs: logs, done: done, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		s.wait(t)
	})
	require.Eventually(t, func() bool {
		m := listenAddrRE.FindStringSubmatch(logs.String())
		if m == nil {
			return false
		}
		s.baseURL = "http://" + m[1]
		return true
	}, 15*time.Second, 20*time.Millisecond,
		"server never logged a listen address; stderr was: %s", logs.String())
	return s
}

// post issues a raw MCP POST, omitting the Authorization header when token is
// empty.
func (s *serving) post(t *testing.T, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.baseURL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"cmd-test","version":"0.0.0"}}}`

// TestHTTPTransportServesThenDrainsToExitZero is the transport's exit-code
// contract end to end through the cobra command: the server binds, answers,
// and when its context is canceled (what SIGINT does to a real invocation) it
// drains and returns no error — exit 0. A stopped server is a completed
// operation, not the interrupted work that exit 130 describes.
func TestHTTPTransportServesThenDrainsToExitZero(t *testing.T) {
	s := serveHTTP(t, &cmdutil.Factory{})

	resp, err := http.Get(s.baseURL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the liveness probe must answer unauthenticated")

	s.cancel()
	exitErr := s.wait(t)
	assert.NoError(t, exitErr, "a completed drain is success")
	assert.Equal(t, 0, cmdutil.ExitCode(exitErr), "an interrupted HTTP server exits 0, not 130")
}

// TestHTTPTransportBindFailureExitsOne pins the other end of the contract: a
// port that cannot be bound is an operator-actionable failure (exit 1), not a
// silent no-op or a usage error.
func TestHTTPTransportBindFailureExitsOne(t *testing.T) {
	// Hold a port, then ask the server for the same one.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	_, _, runErr := run(t, "mcp", "--transport", "http", "--listen", ln.Addr().String())
	require.Error(t, runErr)
	assert.Equal(t, 1, cmdutil.ExitCode(runErr), "a bind failure is a runtime error, not a usage error")
	assert.Contains(t, runErr.Error(), ln.Addr().String())
}

// TestHTTPModeNeverUsesLocalCredentials is the security contract of the whole
// transport: a server that fronts many callers must never fall back to the
// machine's own credential. MELANGE_API_KEY is set to a token the stub
// accepts, and the factory's ApiClient (the only path to the keyring) fails
// the test if it is ever called.
//
//   - a request with no bearer is rejected, rather than served with the local
//     token;
//   - a request with its own bearer reaches the API with that bearer, and the
//     local token never appears upstream.
func TestHTTPModeNeverUsesLocalCredentials(t *testing.T) {
	const localToken = "local-machine-token-do-not-use"
	const requestToken = "caller-token"

	var mu sync.Mutex
	var seen []string
	stub := newMeStub(t, func(bearer string) { mu.Lock(); seen = append(seen, bearer); mu.Unlock() })

	t.Setenv("MELANGE_API_KEY", localToken)
	t.Setenv("MELANGE_HOST", stub)

	f := &cmdutil.Factory{
		ApiClient: func() (*api.Client, error) {
			t.Error("http mode resolved a machine-local credential; it must only ever use the request's bearer")
			return nil, errors.New("must not be called")
		},
	}
	s := serveHTTP(t, f)

	resp := s.post(t, "", initializeBody)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"a credential-less request must be rejected, not served with MELANGE_API_KEY")

	resp = s.post(t, requestToken, initializeBody)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Drive a real tool call so an outgoing API request actually happens.
	callBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`
	resp = s.post(t, requestToken, callBody)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), requestToken+"@example.com",
		"the tool result must come from the request's own identity")

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, seen, "the tool call never reached the API stub")
	for _, bearer := range seen {
		assert.Equal(t, requestToken, bearer,
			"every upstream request must carry the caller's bearer")
		assert.NotEqual(t, localToken, bearer, "the local token must never leave this process")
	}
	assert.NotContains(t, s.logs.String(), requestToken, "bearers must never be logged")
	assert.NotContains(t, s.logs.String(), localToken, "the local token must never be logged")
}

// TestHTTPValidateTokensRejectsAtTheDoor pins that --validate-tokens reaches
// the server: an unknown bearer is refused with 401 before any tool runs,
// where passthrough mode would have accepted the connection and failed later.
func TestHTTPValidateTokensRejectsAtTheDoor(t *testing.T) {
	stub := newMeStub(t, nil)
	t.Setenv("MELANGE_HOST", stub)

	s := serveHTTP(t, &cmdutil.Factory{}, "--validate-tokens")

	resp := s.post(t, "known-token", initializeBody)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a valid bearer is served")

	resp = s.post(t, "", initializeBody)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestHTTPAllowedOriginsReachesTheServer pins that --allowed-origins is wired
// through: the named browser origin is accepted while others are refused.
func TestHTTPAllowedOriginsReachesTheServer(t *testing.T) {
	stub := newMeStub(t, nil)
	t.Setenv("MELANGE_HOST", stub)

	s := serveHTTP(t, &cmdutil.Factory{}, "--allowed-origins", "https://app.zetic.ai")

	req, err := http.NewRequest(http.MethodPost, s.baseURL, strings.NewReader(initializeBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Origin", "https://app.zetic.ai")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the allowlisted origin must be served")

	req.Header.Set("Origin", "https://evil.example")
	req.Body = io.NopCloser(strings.NewReader(initializeBody))
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp2.StatusCode, "other browser origins stay rejected")
}

// newMeStub serves GET /v1/me, echoing the presented bearer back as the
// identity so a test can tell which credential the server used. observe (may
// be nil) records every bearer the stub sees.
func newMeStub(t *testing.T, observe func(bearer string)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if observe != nil {
			observe(bearer)
		}
		if r.URL.Path != "/v1/me" || bearer == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"user":{"email":"%[1]s@example.com","nickname":"%[1]s"},`+
			`"account":{"name":"acct","type":"personal"},`+
			`"token":{"name":"tok","scopes":["read"]}}`, bearer)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
