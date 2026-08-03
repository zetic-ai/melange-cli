package mcp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// authRemediation tells agents how to fix a missing or rejected credential.
const authRemediation = "To fix: run 'melange auth login' or set MELANGE_API_KEY."

// toolError wraps an API or auth failure as an IsError tool result so the
// calling model can see it and self-correct. Go errors returned from handlers
// are reserved for protocol/programming faults; every expected failure goes
// through here. The result is a single text block with no structured content.
func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: toolErrorText(err)}},
	}
}

// toolErrorText renders err with type-specific remediation guidance.
func toolErrorText(err error) string {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return apiErrorText(apiErr)
	}
	var authErr cmdutil.AuthError
	if errors.As(err, &authErr) {
		// No token resolved: same remediation as an authentication_error.
		return err.Error() + "\n" + authRemediation
	}
	// Everything else — including context.Canceled and DeadlineExceeded —
	// passes through as plain error text.
	return err.Error()
}

// apiErrorText renders an *api.Error (type, message, status, request id via
// Error()) plus per-type guidance.
func apiErrorText(e *api.Error) string {
	var b strings.Builder
	b.WriteString(e.Error())
	switch e.Type {
	case "authentication_error":
		b.WriteString("\n" + authRemediation)
	case "permission_error":
		b.WriteString("\nYour token may lack the required scopes; run 'melange auth status' to inspect them.")
	case "rate_limit_error":
		if e.RetryAfter > 0 {
			fmt.Fprintf(&b, "\nRetry after %d seconds.", int(e.RetryAfter.Seconds()))
		}
	case "invalid_request_error":
		for _, f := range e.Fields {
			fmt.Fprintf(&b, "\n- %s: %s", f.Field, f.Message)
		}
	}
	return b.String()
}
