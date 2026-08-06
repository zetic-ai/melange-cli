package mcp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// Default (stdio) remediation text. AuthHints on Deps replaces these for
// transports where local CLI advice would be wrong; the zero value keeps them.
const (
	// stdioUnauthenticatedHint tells a local caller how to fix a missing or
	// rejected credential.
	stdioUnauthenticatedHint = "To fix: run 'melange auth login' or set MELANGE_API_KEY."
	// stdioForbiddenHint tells a local caller how to inspect token scopes.
	stdioForbiddenHint = "Your token may lack the required scopes; run 'melange auth status' to inspect them."
)

// unauthenticated returns the remediation for authentication failures.
func (h AuthHints) unauthenticated() string {
	if h.Unauthenticated != "" {
		return h.Unauthenticated
	}
	return stdioUnauthenticatedHint
}

// forbidden returns the remediation for permission failures.
func (h AuthHints) forbidden() string {
	if h.Forbidden != "" {
		return h.Forbidden
	}
	return stdioForbiddenHint
}

// toolError wraps an API or auth failure as an IsError tool result so the
// calling model can see it and self-correct. Go errors returned from handlers
// are reserved for protocol/programming faults; every expected failure goes
// through here. The result is a single text block with no structured content.
// Remediation text follows d.AuthHints (zero value = stdio defaults).
func (d Deps) toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: d.toolErrorText(err)}},
	}
}

// toolErrorText renders err with type-specific remediation guidance.
func (d Deps) toolErrorText(err error) string {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return d.apiErrorText(apiErr)
	}
	var authErr cmdutil.AuthError
	if errors.As(err, &authErr) {
		// No token resolved: same remediation as an authentication_error.
		return err.Error() + "\n" + d.AuthHints.unauthenticated()
	}
	// Everything else — including context.Canceled and DeadlineExceeded —
	// passes through as plain error text.
	return err.Error()
}

// apiErrorText renders an *api.Error (type, message, status, request id via
// Error()) plus per-type guidance.
func (d Deps) apiErrorText(e *api.Error) string {
	var b strings.Builder
	b.WriteString(e.Error())
	switch e.Type {
	case "authentication_error":
		b.WriteString("\n" + d.AuthHints.unauthenticated())
	case "permission_error":
		b.WriteString("\n" + d.AuthHints.forbidden())
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
