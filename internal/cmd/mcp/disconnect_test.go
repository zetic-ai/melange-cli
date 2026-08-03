package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsCleanDisconnect pins which Run() outcomes mean "stop serving, without
// fault" (exit 0) versus "something went wrong" (non-zero). The distinction
// matters because an agent client that closes stdin mid-tool-call must not be
// reported to the user as a crashed MCP server, while a genuine protocol fault
// or a SIGINT must keep its exit code.
//
// This table is a classification test only: its ErrServerClosing case is built
// from the same local constant the production code compares against, so it says
// nothing about whether the SDK still produces that shape.
// TestIsCleanDisconnectAcceptsRealHangup is what ties the constant to the SDK.
func TestIsCleanDisconnect(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"served to completion", nil, true},
		{"stdin EOF", io.EOF, true},
		{"wrapped stdin EOF", fmt.Errorf("read stdin: %w", io.EOF), true},
		{
			name: "shutdown abandoned in-flight work",
			err:  fmt.Errorf("%w: %v", &jsonrpc.Error{Code: codeServerClosing, Message: "server is closing"}, io.EOF),
			want: true,
		},
		{"interrupted", context.Canceled, false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"protocol fault", errors.New("malformed frame"), false},
		{
			name: "internal jsonrpc error",
			err:  &jsonrpc.Error{Code: -32603, Message: "internal error"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isCleanDisconnect(tt.err))
		})
	}
}

// blockArgs is the (empty) argument type of the test-only blocking tool.
type blockArgs struct{}

// TestIsCleanDisconnectAcceptsRealHangup is the behavioral half of the
// contract: it drives a real go-sdk server over a pipe, hangs the client up
// while a tool call is still in flight, and feeds whatever Run() actually
// returns through isCleanDisconnect.
//
// Without this, codeServerClosing would be pinned only against itself: an SDK
// upgrade that changed the shape or code of the abandoned-in-flight error would
// leave every unit case green while `melange mcp` silently went back to exiting
// 1 on every client shutdown that lands mid-request. Here the SDK is the source
// of the error, so such a change fails the test instead.
func TestIsCleanDisconnectAcceptsRealHangup(t *testing.T) {
	stdin, clientWrites := io.Pipe()

	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "melange-test", Version: "test"}, nil)
	started := make(chan struct{})
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:        "block",
		Description: "Stays in flight until the connection drops.",
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ blockArgs) (*mcpsdk.CallToolResult, any, error) {
		close(started)
		// The SDK cancels in-flight handlers once the reader is gone, so this
		// returns exactly when the hangup has been observed — no sleeps, and
		// the reply is attempted strictly after the connection started closing.
		<-ctx.Done()
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "too late"}},
		}, nil, nil
	})

	runErr := make(chan error, 1)
	go func() {
		runErr <- srv.Run(context.Background(), &mcpsdk.IOTransport{
			Reader: stdin,
			Writer: discardWriteCloser{},
		})
	}()

	for _, frame := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"hangup","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"block","arguments":{}}}`,
	} {
		_, err := io.WriteString(clientWrites, frame+"\n")
		require.NoError(t, err)
	}

	<-started
	// The client goes away mid-call, exactly as `printf … | melange mcp` and as
	// an agent client shutting the server down do.
	require.NoError(t, clientWrites.Close())

	var err error
	select {
	case err = <-runErr:
	case <-time.After(30 * time.Second):
		t.Fatal("server did not stop after the client hung up")
	}

	// Guard: if a future SDK returns nil here the assertion below would pass
	// vacuously, and this test would stop covering anything.
	require.Error(t, err, "expected the SDK to report the abandoned in-flight reply")
	assert.True(t, isCleanDisconnect(err),
		"a client hangup mid-request must exit 0, but the SDK's error %#v was classified as a failure", err)
}

// discardWriteCloser is a stdout stand-in that accepts and drops everything;
// the test only cares about what Run() returns, not the frames.
type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }
