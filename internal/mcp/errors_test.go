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
	"github.com/zetic-ai/melange-cli/internal/edition"
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
			res := Deps{}.toolError(tt.err)
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
	res := Deps{}.toolError(&api.Error{
		StatusCode: 401, Type: "authentication_error",
		Message: "invalid token", RequestID: "req_1",
	})
	want := "melange API: invalid token (authentication_error, HTTP 401, request req_1)\n" +
		"To fix: run 'melange auth login' or set MELANGE_API_KEY."
	assert.Equal(t, want, textOf(t, res))
}

func TestQualcommToolErrorUsesEditionBranding(t *testing.T) {
	res := (Deps{Edition: edition.Qualcomm()}).toolError(&api.Error{
		StatusCode: 401, Type: "authentication_error",
		Message: "invalid token", RequestID: "req_1",
	})
	want := "melange-qcom API: invalid token (authentication_error, HTTP 401, request req_1)\n" +
		"To fix: run 'melange-qcom auth login' or set MELANGE_API_KEY."
	assert.Equal(t, want, textOf(t, res))
}

func TestToolErrorCustomAuthHints(t *testing.T) {
	// The AuthHints seam: a transport that supplies its own remediation text
	// must see it verbatim in place of the stdio defaults, on every path that
	// renders an auth hint.
	d := Deps{AuthHints: AuthHints{
		Unauthenticated: "HTTP-UNAUTH-HINT: reconnect with a fresh token.",
		Forbidden:       "HTTP-SCOPE-HINT: mint a token with more scopes.",
	}}

	t.Run("authentication_error uses the Unauthenticated hint", func(t *testing.T) {
		res := d.toolError(&api.Error{
			StatusCode: 401, Type: "authentication_error",
			Message: "invalid token", RequestID: "req_1",
		})
		want := "melange API: invalid token (authentication_error, HTTP 401, request req_1)\n" +
			"HTTP-UNAUTH-HINT: reconnect with a fresh token."
		assert.Equal(t, want, textOf(t, res))
	})

	t.Run("permission_error uses the Forbidden hint", func(t *testing.T) {
		res := d.toolError(&api.Error{
			StatusCode: 403, Type: "permission_error",
			Message: "token lacks the write scope", RequestID: "req_2",
		})
		text := textOf(t, res)
		assert.Contains(t, text, "HTTP-SCOPE-HINT: mint a token with more scopes.")
		assert.NotContains(t, text, "melange auth status")
	})

	t.Run("unresolved token uses the Unauthenticated hint", func(t *testing.T) {
		res := d.toolError(cmdutil.AuthError{Err: errors.New("no token")})
		text := textOf(t, res)
		assert.Contains(t, text, "HTTP-UNAUTH-HINT: reconnect with a fresh token.")
		assert.NotContains(t, text, "MELANGE_API_KEY")
	})

	t.Run("partial hints fall back per field", func(t *testing.T) {
		partial := Deps{AuthHints: AuthHints{Unauthenticated: "CUSTOM-401."}}
		res := partial.toolError(&api.Error{StatusCode: 403, Type: "permission_error", Message: "nope"})
		assert.Contains(t, textOf(t, res),
			"Your token may lack the required scopes; run 'melange auth status' to inspect them.")
	})
}
