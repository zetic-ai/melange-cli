package mcp

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// textOf asserts the result is a single text block and returns its text.
func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.Len(t, res.Content, 1, "tool errors are a single content block")
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok, "tool errors are text content")
	return tc.Text
}

func TestToolErrorMapping(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantText    []string
		notWantText []string
	}{
		{
			name: "authentication error includes remediation",
			err: &api.Error{
				StatusCode: 401, Type: "authentication_error",
				Message: "invalid token", RequestID: "req_1",
			},
			wantText: []string{
				"melange API: invalid token (authentication_error, HTTP 401, request req_1)",
				"To fix: run 'melange auth login' or set MELANGE_API_KEY.",
			},
		},
		{
			name: "permission error mentions token scopes",
			err: &api.Error{
				StatusCode: 403, Type: "permission_error",
				Message: "token lacks the write scope", RequestID: "req_2",
			},
			wantText: []string{
				"token lacks the write scope",
				"scopes",
				"melange auth status",
			},
			notWantText: []string{"MELANGE_API_KEY"},
		},
		{
			name: "rate limit error includes Retry-After seconds",
			err: &api.Error{
				StatusCode: 429, Type: "rate_limit_error",
				Message: "too many requests", RetryAfter: 30 * time.Second,
			},
			wantText: []string{"too many requests", "Retry after 30 seconds."},
		},
		{
			name: "rate limit error without Retry-After omits the hint",
			err: &api.Error{
				StatusCode: 429, Type: "rate_limit_error",
				Message: "too many requests",
			},
			wantText:    []string{"too many requests"},
			notWantText: []string{"Retry after"},
		},
		{
			name: "invalid request error lists field errors",
			err: &api.Error{
				StatusCode: 400, Type: "invalid_request_error",
				Message: "validation failed",
				Fields: []api.FieldError{
					{Field: "name", Message: "is required"},
					{Field: "model_type", Message: "must be one of general, llm"},
				},
			},
			wantText: []string{
				"validation failed",
				"- name: is required",
				"- model_type: must be one of general, llm",
			},
		},
		{
			name: "other api error carries type, message, and request id",
			err: &api.Error{
				StatusCode: 404, Type: "not_found_error",
				Message: "repository zetic/nope not found", RequestID: "req_9",
			},
			wantText: []string{
				"melange API: repository zetic/nope not found (not_found_error, HTTP 404, request req_9)",
			},
			notWantText: []string{"To fix", "Retry after"},
		},
		{
			name: "no token resolved gets the auth remediation",
			err:  cmdutil.AuthError{Err: errors.New("not logged in to api.zetic.ai")},
			wantText: []string{
				"not logged in to api.zetic.ai",
				"To fix: run 'melange auth login' or set MELANGE_API_KEY.",
			},
		},
		{
			name: "wrapped auth error still matches",
			err:  fmt.Errorf("resolving client: %w", cmdutil.AuthError{Err: errors.New("no token")}),
			wantText: []string{
				"resolving client: no token",
				"To fix: run 'melange auth login' or set MELANGE_API_KEY.",
			},
		},
		{
			name:        "context canceled passes through as plain text",
			err:         context.Canceled,
			wantText:    []string{"context canceled"},
			notWantText: []string{"To fix"},
		},
		{
			name:        "deadline exceeded passes through as plain text",
			err:         context.DeadlineExceeded,
			wantText:    []string{"context deadline exceeded"},
			notWantText: []string{"To fix"},
		},
		{
			name:        "generic error passes through as plain text",
			err:         errors.New("dial tcp: connection refused"),
			wantText:    []string{"dial tcp: connection refused"},
			notWantText: []string{"To fix"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := toolError(tt.err)
			assert.True(t, res.IsError, "toolError must set IsError")
			assert.Nil(t, res.StructuredContent, "tool errors are text-only")
			text := textOf(t, res)
			for _, want := range tt.wantText {
				assert.Contains(t, text, want)
			}
			for _, notWant := range tt.notWantText {
				assert.NotContains(t, text, notWant)
			}
		})
	}
}

func TestToolErrorExactAuthText(t *testing.T) {
	// Pin the full rendering once so remediation placement can't drift.
	res := toolError(&api.Error{
		StatusCode: 401, Type: "authentication_error",
		Message: "invalid token", RequestID: "req_1",
	})
	want := "melange API: invalid token (authentication_error, HTTP 401, request req_1)\n" +
		"To fix: run 'melange auth login' or set MELANGE_API_KEY."
	assert.Equal(t, want, textOf(t, res))
}
