package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// OAuth token scopes as the Melange authorization server mints them. Scopes
// arrive AS GRANTED: a token granted ["write"] does not necessarily carry
// "read", so scopeSatisfied encodes write-implies-read instead of expecting
// the grant to.
const (
	scopeRead  = "read"
	scopeWrite = "write"
)

// requireScope is the per-tool scope gate every mutating tool calls before
// doing anything else. It returns nil when the call may proceed, or a
// toolError-shaped refusal that the handler must return as-is — the refusal is
// produced before any API request, so a caller holding a read-only token
// learns exactly what to do without a byte leaving the server.
//
// Where the scopes come from (verified against go-sdk v1.7.0 source): the
// SDK's RequireBearerToken stores the verifier's *auth.TokenInfo on the HTTP
// request context (auth/auth.go), the stateless streamable handler seeds each
// per-request connection with that context (mcp/streamable.go:
// connectStreamable(req.Context(), ...)), and jsonrpc2 handler contexts derive
// from the connection root through a value-preserving wrapper
// (internal/jsonrpc2 notDone, whose Value delegates). So
// auth.TokenInfoFromContext works inside tool handlers with no extra plumbing.
//
// Enforcement is deliberately conditional on a scope-POPULATING verifier
// having run, which two absent cases distinguish:
//
//   - No TokenInfo at all: the stdio (and in-memory) transport, where the
//     local user's own credential rides the outgoing client and the API plus
//     the CLI's own errors are the authority. Stdio behavior is frozen;
//     nothing is enforced.
//   - TokenInfo with no scopes: the HTTP passthrough posture —
//     PassthroughVerifier validates nothing and returns an empty TokenInfo.
//     Refusing on an empty scope set would 403 every mutating call for every
//     valid PAT, the exact trap PR2's review flagged for the global
//     RequireBearerTokenOptions.Scopes (which stays unset for the same
//     reason); the API remains the sole authority.
//
// When MeVerifier ran, TokenInfo.Scopes carries the /v1/me-reported grant for
// both credential kinds (ztp_ PATs and zoa_ OAuth tokens), and the gate
// enforces it. A validated token whose grant somehow names no scopes falls
// back to the API's own authorization — this gate narrows known grants, it
// never invents an authority the backend didn't state.
func (d Deps) requireScope(ctx context.Context, scope string) *mcp.CallToolResult {
	info := auth.TokenInfoFromContext(ctx)
	if info == nil || len(info.Scopes) == 0 {
		return nil
	}
	if scopeSatisfied(info.Scopes, scope) {
		return nil
	}
	return d.toolError(errors.New(scopeRefusalText(scope, info.Scopes)))
}

// scopeRefusalText is the agent-actionable refusal for a scope-blocked call.
// It is the single source of that text: requireScope returns it as a tool
// error (the in-band backstop), and the HTTP transport's insufficient_scope
// gate (httpserver) sends the same bytes in its RFC 6750 403 body, so an
// agent reads identical remediation whichever layer refused.
//
// Scope names are grant vocabulary ("read", "write"), never credential bytes,
// so echoing them is hygienic and tells the agent precisely what
// re-authorization to ask for.
func scopeRefusalText(scope string, granted []string) string {
	return fmt.Sprintf(
		"insufficient scope: this operation needs the %q scope, but the token was granted only %q. "+
			"No API request was made. Re-authorize granting the %q scope "+
			"(or reconnect with a credential that includes it), then call the tool again",
		scope, strings.Join(granted, " "), scope)
}

// writeScopeTools names every tool whose handler gates on the write scope —
// the catalog's mutating tools. The HTTP transport's insufficient_scope gate
// consumes this via RequiresWriteScope to answer a genuinely scope-blocked
// tools/call with an RFC 6750 403 before the streamable handler runs.
// TestRequiresWriteScopeMatchesCatalog pins this set to the registered
// catalog's ReadOnlyHint annotations, so a new mutating tool cannot ship
// without joining it (and TestMutatingToolCasesCoverCatalog already forces it
// to carry the in-handler gate).
var writeScopeTools = map[string]bool{
	"create_repo":            true,
	"update_repo":            true,
	"delete_repo":            true,
	"set_default_model":      true,
	"import_model":           true,
	"request_model_download": true,
	// upload_model is stdio-only (HTTP never serves it), so over HTTP a
	// read-scoped OAuth call to it draws an insufficient_scope 403 — and a
	// client that steps up and retries then learns the tool does not exist,
	// where skipping the entry would have said "unknown tool" immediately.
	// That misdirection is accepted deliberately: the set's invariant is
	// "equal to the catalog's non-read-only tools" (pinned by
	// TestRequiresWriteScopeMatchesCatalog over the local superset), and
	// carving a transport-specific exception here would trade a rare, mildly
	// slower error path for a second, driftable source of truth.
	"upload_model": true,
}

// RequiresWriteScope reports whether tool is a mutating tool that gates on
// the write scope.
func RequiresWriteScope(tool string) bool { return writeScopeTools[tool] }

// WriteScopeGranted reports whether a granted scope set covers the write
// scope, with exactly the semantics of the in-handler gate.
func WriteScopeGranted(granted []string) bool { return scopeSatisfied(granted, scopeWrite) }

// WriteScopeRefusalText is the write-scope refusal message for a token
// granted only the given scopes — byte-identical to the tool-error text
// requireScope produces, so the HTTP 403 body and the in-band refusal can
// never drift apart.
func WriteScopeRefusalText(granted []string) string {
	return scopeRefusalText(scopeWrite, granted)
}

// scopeSatisfied reports whether the granted scope set covers need. Write
// implies read: a token trusted to change a resource is trusted to see it, and
// the authorization server grants scopes verbatim without expanding that
// implication itself.
func scopeSatisfied(granted []string, need string) bool {
	for _, s := range granted {
		if s == need || (need == scopeRead && s == scopeWrite) {
			return true
		}
	}
	return false
}
