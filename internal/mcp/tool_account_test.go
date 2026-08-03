package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// meBody uses non-alphabetical key order so any re-marshal (which would sort
// map keys) breaks byte-equality assertions.
const meBody = `{"user":{"email":"dev@zetic.ai","nickname":"dev"},` +
	`"account":{"name":"zetic","type":"org"},` +
	`"token":{"name":"ci-token","scopes":["read","write"]}}`

// syncWriter guards the wire log: transport reads are concurrent to writes.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// registryProvider returns a ClientProvider whose generated client talks to
// the httpmock registry.
func registryProvider(t *testing.T, reg *httpmock.Registry) ClientProvider {
	t.Helper()
	return NewStaticProvider(func() (*gen.ClientWithResponses, error) {
		return gen.NewClientWithResponses("https://api.zetic.ai",
			gen.WithHTTPClient(&http.Client{Transport: reg}))
	})
}

// connect wires a fresh server for provider to an in-memory client session
// and returns the session plus the client-side wire log.
func connect(t *testing.T, provider ClientProvider) (*mcp.ClientSession, *syncWriter) {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	srv := New(Deps{Clients: provider, Version: "test"}, Options{})
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Wait() })

	wire := &syncWriter{}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx,
		&mcp.LoggingTransport{Transport: clientTransport, Writer: wire}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession, wire
}

func TestWhoamiRoundTripPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"),
		httpmock.JSONResponse(200, json.RawMessage(meBody)))

	cs, wire := connect(t, registryProvider(t, reg))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	require.NoError(t, err)

	assert.False(t, res.IsError)
	assert.Equal(t, meBody, textOf(t, res), "text mirror carries the exact body")

	structured, merr := json.Marshal(res.StructuredContent)
	require.NoError(t, merr)
	assert.JSONEq(t, meBody, string(structured))

	// Byte-exactness across the wire: the raw response frame must embed the
	// stubbed body verbatim (key order intact — no typed re-marshal).
	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+meBody)

	reg.Verify(t)
}

func TestWhoamiAPIAuthErrorIsToolErrorWithRemediation(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"),
		httpmock.JSONResponse(401, json.RawMessage(
			`{"type":"error","error":{"type":"authentication_error","message":"invalid token"},"request_id":"req_1"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	require.NoError(t, err, "API failures are tool errors, not protocol errors")

	assert.True(t, res.IsError)
	text := textOf(t, res)
	assert.Contains(t, text, "invalid token")
	assert.Contains(t, text, "melange auth login")
	assert.Contains(t, text, "MELANGE_API_KEY")
}

func TestWhoamiNoTokenResolvedIsToolErrorWithRemediation(t *testing.T) {
	provider := NewStaticProvider(func() (*gen.ClientWithResponses, error) {
		return nil, cmdutil.AuthError{Err: errors.New("not logged in to api.zetic.ai")}
	})

	cs, _ := connect(t, provider)
	// Twice: the cached resolve failure must surface on every call.
	for range 2 {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
		require.NoError(t, err)
		assert.True(t, res.IsError)
		text := textOf(t, res)
		assert.Contains(t, text, "not logged in to api.zetic.ai")
		assert.Contains(t, text, "melange auth login")
		assert.Contains(t, text, "MELANGE_API_KEY")
	}
}

func TestWhoamiToolAnnotations(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	var whoami *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "whoami" {
			whoami = tool
		}
	}
	require.NotNil(t, whoami, "whoami must be registered")
	assert.False(t, strings.Contains(whoami.Name, "melange"), "tool names are unprefixed")
	require.NotNil(t, whoami.Annotations)
	assert.True(t, whoami.Annotations.ReadOnlyHint)
	assert.True(t, whoami.Annotations.IdempotentHint)
	require.NotNil(t, whoami.Annotations.DestructiveHint, "DestructiveHint must be set explicitly (SDK default is true)")
	assert.False(t, *whoami.Annotations.DestructiveHint)
	assert.Nil(t, whoami.OutputSchema, "no output schema until Task 5 (Out = any)")
}
