package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/assert"
)

// TestIsCleanDisconnect pins which Run() outcomes mean "the client hung up"
// (exit 0) versus "something went wrong" (non-zero). The distinction matters
// because an agent client that closes stdin mid-tool-call must not be reported
// to the user as a crashed MCP server, while a genuine protocol fault or a
// SIGINT must keep its exit code.
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
			// What a one-shot `printf ... | melange mcp` produces: stdin hits
			// EOF before the reply to an in-flight request is written.
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
