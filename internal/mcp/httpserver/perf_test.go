package httpserver

// Performance & robustness suite (CI-gated, deterministic): high-concurrency
// token isolation, goroutine hygiene after completed and abandoned sessions,
// and the forced-close drain path. Nothing here asserts on wall-clock timing;
// every synchronization point is a channel or a bounded wait on one. The
// on-demand load harness lives in tools/mcploadgen + script/mcp-loadtest.sh.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mcpserver "github.com/zetic-ai/melange-cli/internal/mcp"
	"go.uber.org/goleak"
)

// TestPerRequestServersShareOneSchemaCache guards this transport's half of
// the schema-cache wiring: getServer must hand every per-request server the
// process-wide cache created in New. The internal/mcp allocation guard cannot
// see this seam — if getServer stopped passing s.schemaCache, every request
// would silently re-resolve every schema and all other tests would stay
// green. Allocation counting keeps the guard deterministic and wall-clock-
// free: a warmed getServer construction allocates ~2k objects, an uncached
// server construction ~60k, so the loose 10x threshold cannot flake yet fails
// the moment either the getServer wiring or mcpserver.New's pass-through to
// the SDK disappears.
func TestPerRequestServersShareOneSchemaCache(t *testing.T) {
	srv, err := New(Config{Listen: "127.0.0.1:0", APIHost: "http://127.0.0.1:1"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), bearerContextKey{}, "token-alloc-guard"))

	require.NotNil(t, srv.getServer(req), "the warmup request must build a server")

	baseline := testing.AllocsPerRun(5, func() {
		mcpserver.New(mcpserver.Deps{Version: "alloc-guard"}, mcpserver.Options{})
	})
	warm := testing.AllocsPerRun(5, func() { srv.getServer(req) })

	assert.Less(t, warm, baseline/10,
		"per-request servers must reuse the shared schema cache: %.0f allocs per getServer vs %.0f uncached",
		warm, baseline)
}

// TestConcurrentSessionsTokenIsolation runs 32 parallel sessions × 16 calls
// each through the real HTTP stack under -race. Every response must be valid
// and carry exactly the identity of the session's own bearer: the /v1/me stub
// only produces identity i for token i, so any crossed credential — a
// per-request server observing another request's bearer — surfaces as the
// wrong identity (or a 401) in some session's results.
//
// The token limiter runs at PRODUCTION tuning on purpose: a session's 17-odd
// requests must fit the real per-token burst, so this test also pins that the
// shipped budget accommodates realistic session traffic. Only the pre-auth IP
// limiter is scaled up (cfg.ipLimit): httptest pins all 500+ requests to
// 127.0.0.1, one bucket, and this test is not about the anti-spray budget —
// TestIPRateLimitStopsTokenSpray is.
func TestConcurrentSessionsTokenIsolation(t *testing.T) {
	const (
		sessions        = 32
		callsPerSession = 16
	)

	users := make(map[string]string, sessions)
	for i := 0; i < sessions; i++ {
		users[fmt.Sprintf("token-%02d", i)] = fmt.Sprintf("user%02d", i)
	}
	stub := newMeStub(t, users)
	_, ts := newTestServer(t, stub.URL, func(c *Config) {
		c.ipLimit = &limitPolicy{
			burst:           4 * sessions * callsPerSession,
			refillPerSecond: ipRateLimitRefillPerSecond,
			key:             remoteIPKey,
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			me := fmt.Sprintf("user%02d@example.com", i)
			session := connectSession(t, ctx, ts.URL, fmt.Sprintf("token-%02d", i))
			for c := 0; c < callsPerSession; c++ {
				text, isErr, err := callWhoami(ctx, session)
				if !assert.NoError(t, err, "session %d call %d", i, c) {
					return
				}
				if !assert.False(t, isErr, "session %d call %d: unexpected tool error: %s", i, c, text) {
					return
				}
				assert.Contains(t, text, me, "session %d call %d must see its own identity", i, c)
				for other := 0; other < sessions; other++ {
					if other != i && strings.Contains(text, fmt.Sprintf("user%02d@", other)) {
						t.Errorf("session %d call %d leaked session %d's identity: %s", i, c, other, text)
					}
				}
			}
			assert.NoError(t, session.Close())
		}()
	}
	wg.Wait()
}

// gatedMeStub is an API stub whose /v1/me signals inflight and then blocks
// until released, making "a tool call is provably in flight" a channel receive
// instead of a guess.
type gatedMeStub struct {
	*httptest.Server
	// inflight receives one value per handler that reached the gate.
	inflight chan struct{}
	// release is closed (via releaseAll) to unblock every gated handler.
	release     chan struct{}
	releaseOnce sync.Once
}

// newGatedMeStub builds the stub. Cleanups run LIFO, so the gate is always
// released before Close waits on outstanding handlers — a failing test cannot
// hang the suite.
func newGatedMeStub(t *testing.T) *gatedMeStub {
	t.Helper()
	s := &gatedMeStub{
		inflight: make(chan struct{}, 64),
		release:  make(chan struct{}),
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.inflight <- struct{}{}
		<-s.release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, meBody("ana"))
	}))
	t.Cleanup(s.Close)
	t.Cleanup(s.releaseAll)
	return s
}

func (s *gatedMeStub) releaseAll() {
	s.releaseOnce.Do(func() { close(s.release) })
}

// verifyNoLeaks closes every server-side and client-side resource the test
// stack holds open and then requires the goroutine count to return to
// baseline.
//
// No IgnoreTopFunction allowances are needed, and that is verified against
// go-sdk v1.7.0 source, not assumed: in stateless mode serveStateless creates
// a per-request session and closes it before returning (mcp/streamable.go:
// `defer session.Close()`), so no session goroutine outlives its HTTP request.
// The SDK's one documented by-design leak — the ioConn reader that may block
// forever on a closed stdin (mcp/transport.go, newIOConn) — is on the stdio
// transport path only, which HTTP mode never touches. What DOES linger without
// explicit teardown is net/http itself: kept-alive connection goroutines on
// both hops (client→MCP server, MCP server→API stub). Those belong to the
// test topology, not the code under test, so they are shut down here rather
// than ignored: closing both httptest servers severs every connection (their
// Close blocks until handlers return and conns are gone), and
// CloseIdleConnections reaps the client-side pool of http.DefaultTransport —
// which is also the base transport inside api.NewClient, covering the
// server→stub hop.
func verifyNoLeaks(t *testing.T, ts, stub *httptest.Server) {
	t.Helper()
	stub.Close()
	ts.Close()
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	goleak.VerifyNone(t)
}

// TestNoGoroutineLeaksAfterCompletedSessions pins goroutine hygiene on the
// happy path: N concurrent sessions each complete several tool calls and
// close; once the stack is torn down, no goroutine may remain.
func TestNoGoroutineLeaksAfterCompletedSessions(t *testing.T) {
	const sessions, calls = 8, 4 // 8×(1 init + 4 calls) = 40 ≤ the per-IP burst

	users := make(map[string]string, sessions)
	for i := 0; i < sessions; i++ {
		users[fmt.Sprintf("token-%02d", i)] = fmt.Sprintf("user%02d", i)
	}
	stub := newMeStub(t, users)
	_, ts := newTestServer(t, stub.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session := connectSession(t, ctx, ts.URL, fmt.Sprintf("token-%02d", i))
			for c := 0; c < calls; c++ {
				text, isErr, err := callWhoami(ctx, session)
				if !assert.NoError(t, err, "session %d call %d", i, c) {
					return
				}
				assert.False(t, isErr, "session %d call %d: %s", i, c, text)
			}
			assert.NoError(t, session.Close())
		}()
	}
	wg.Wait()

	verifyNoLeaks(t, ts, stub)
}

// rawToolsCallConn opens a raw TCP connection to the MCP server and writes a
// complete, self-contained tools/call POST (stateless mode synthesizes the
// initialize handshake per request, verified in go-sdk v1.7.0
// serveStateless/ephemeralConnectOpts). The caller owns the connection — this
// is exactly the primitive an abandonment test needs, because an *http.Client
// offers no way to slam the socket mid-response.
func rawToolsCallConn(t *testing.T, hostport, token string) net.Conn {
	t.Helper()
	const body = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`
	conn, err := net.Dial("tcp", hostport)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	req := fmt.Sprintf("POST / HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n"+
		"Content-Type: application/json\r\nAccept: application/json, text/event-stream\r\n"+
		"Content-Length: %d\r\n\r\n%s", hostport, token, len(body), body)
	_, err = io.WriteString(conn, req)
	require.NoError(t, err)
	return conn
}

// TestNoGoroutineLeaksAfterAbandonedSessions pins goroutine hygiene on the
// rude path: M clients slam their TCP connections shut while a tools/call is
// blocked mid-flight in the backend. The server must unwind every per-request
// goroutine anyway — an abandoned call may not strand a session, a jsonrpc2
// connection, or an outgoing API request.
func TestNoGoroutineLeaksAfterAbandonedSessions(t *testing.T) {
	const abandoned = 4 // 4 requests ≤ the per-IP burst

	stub := newGatedMeStub(t)
	_, ts := newTestServer(t, stub.URL, nil)
	hostport := strings.TrimPrefix(ts.URL, "http://")

	// Drive each call provably into the backend before abandoning it.
	conns := make([]net.Conn, 0, abandoned)
	for i := 0; i < abandoned; i++ {
		conns = append(conns, rawToolsCallConn(t, hostport, fmt.Sprintf("token-%02d", i)))
		select {
		case <-stub.inflight:
		case <-time.After(10 * time.Second):
			t.Fatalf("call %d never reached the API stub", i)
		}
	}
	for _, c := range conns {
		require.NoError(t, c.Close(), "client-side TCP close mid tools/call")
	}

	// Release the gate so the stub handlers can finish; the MCP handlers then
	// discover their dead clients and unwind. Both Closes inside verifyNoLeaks
	// block until every handler has returned, so VerifyNone observes the
	// settled state (and retries briefly on top of that).
	stub.releaseAll()
	verifyNoLeaks(t, ts, stub.Server)
}

// TestDrainForcedCloseWhenDeadlineExceeded exercises the drain overrun
// contract end to end: a tool call still blocked when the (test-shortened)
// drain deadline expires forces Shutdown to give up, remaining connections are
// force-closed, and ListenAndServe returns an error — the exit-1 path. The
// complete-inside-the-window counterpart is TestShutdownDrainsInflightRequests.
//
// Deterministic by construction: the gate stays held until AFTER the serve
// error is observed, so the drain cannot possibly finish cleanly, however slow
// the machine.
func TestDrainForcedCloseWhenDeadlineExceeded(t *testing.T) {
	stub := newGatedMeStub(t)

	srv, err := New(Config{
		Listen:    "127.0.0.1:0",
		APIHost:   stub.URL,
		UserAgent: "melange-cli-test/0.0.0",
		Version:   "v0.0.0-test",
	})
	require.NoError(t, err)
	srv.drainTimeout = 50 * time.Millisecond

	serveCtx, stop := context.WithCancel(context.Background())
	defer stop()
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe(serveCtx) }()
	require.Eventually(t, func() bool { return srv.Addr() != nil },
		5*time.Second, 10*time.Millisecond, "server never bound its listener")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connectSession(t, ctx, "http://"+srv.Addr().String(), "token-a")

	callErr := make(chan error, 1)
	go func() {
		_, _, err := callWhoami(ctx, session)
		callErr <- err
	}()

	select {
	case <-stub.inflight:
	case <-time.After(10 * time.Second):
		t.Fatal("tool call never reached the API stub")
	}
	stop() // begin shutdown with the call still gated: the drain MUST overrun

	select {
	case err := <-served:
		require.Error(t, err, "an overrun drain must return an error (exit 1)")
		assert.Contains(t, err.Error(), "force-closed")
	case <-time.After(10 * time.Second):
		t.Fatal("ListenAndServe did not return after the drain deadline")
	}

	// The abandoned caller sees a transport failure, not a stuck call.
	select {
	case err := <-callErr:
		assert.Error(t, err, "a force-closed connection must fail the in-flight call")
	case <-time.After(10 * time.Second):
		t.Fatal("in-flight call neither completed nor failed after forced close")
	}
}
