package httpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpserver "github.com/zetic-ai/melange-cli/internal/mcp"
)

// This file adds the RFC 6750 machine-readable half of scope enforcement: a
// genuinely scope-blocked tools/call is answered 403 with
// `WWW-Authenticate: Bearer error="insufficient_scope", scope="write"`, the
// trigger a step-up-capable OAuth client needs to re-authorize with more
// scope (go-sdk v1.7.0's streamable client, for one, runs its OAuth handler
// on a 401/403 response and retries the request — mcp/streamable.go).
//
// Why this is middleware and not SDK configuration: the SDK's only scope knob
// is RequireBearerTokenOptions.Scopes, which its verify() enforces against
// EVERY request through the middleware — under PassthroughVerifier (empty
// TokenInfo) that 403s everything, the exact trap PR2's review flagged, and
// even under MeVerifier it would block read-only tools/list for a read
// token. Tool handlers, at the other end, run inside the JSON-RPC layer with
// no access to the HTTP response, so a per-tool 403 can only come from a
// layer that sees both the HTTP exchange and the request's target tool. In
// stateless mode every POST body is one self-contained JSON-RPC message, so
// peeking at it is cheap and exact.
//
// The gate is deliberately fail-open ONLY toward the in-band backstop: any
// request it cannot positively classify as a scope-blocked write-tool call
// (non-POST, unparseable body, unknown method or tool, no populated scopes)
// passes through untouched, where requireScope inside the tool handler still
// refuses scope-blocked calls with the same text as a tool error. Enforcement
// is therefore never weaker than before this gate existed — only the signal
// is richer.

// insufficientScopeCode is the JSON-RPC error code carried in the 403 body.
// JSON-RPC 2.0 reserves -32000..-32099 for implementation-defined server
// errors; the concrete value is uncontracted (clients key on the HTTP status
// and WWW-Authenticate header — the message text is for the agent).
const insufficientScopeCode = -32000

// scopeStepUpGate answers scope-blocked write-tool calls with a 403 +
// WWW-Authenticate challenge instead of letting the handler produce an
// in-band tool error. It must only be installed behind a scope-POPULATING
// verifier (MeVerifier): New wires it exactly when verifierKind is "me", and
// the len(Scopes) > 0 guard below keeps even a misplaced installation from
// 403ing the passthrough posture. resourceMetadataURL, when non-empty, rides
// the challenge so a step-up client can discover the authorization server
// from the 403 alone (it is the same URL the 401 challenge advertises).
func scopeStepUpGate(resourceMetadataURL string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		info := auth.TokenInfoFromContext(r.Context())
		if info == nil || len(info.Scopes) == 0 {
			// No verified grant to enforce (PassthroughVerifier's empty
			// TokenInfo, or a validated token whose grant names no scopes):
			// the API stays the sole authority, exactly as in requireScope.
			next.ServeHTTP(w, r)
			return
		}
		body := peekBody(r)
		if body == nil {
			next.ServeHTTP(w, r)
			return
		}
		id, ok := writeToolCall(body)
		if !ok || mcpserver.WriteScopeGranted(info.Scopes) {
			next.ServeHTTP(w, r)
			return
		}
		writeInsufficientScope(w, resourceMetadataURL, id, info.Scopes)
	})
}

// peekBody reads the request body (bounded by the same cap the streamable
// handler enforces) and hands back the bytes, reattaching an equivalent body
// so the next handler reads exactly what arrived. A nil result means the body
// could not be classified (read error or over the cap): the caller must pass
// the request through and let the SDK produce its own error.
func peekBody(r *http.Request) []byte {
	original := r.Body
	buf, err := io.ReadAll(io.LimitReader(original, maxRequestBodyBytes+1))
	r.Body = replayBody{Reader: io.MultiReader(bytes.NewReader(buf), original), Closer: original}
	if err != nil || len(buf) > maxRequestBodyBytes {
		return nil
	}
	return buf
}

// replayBody prefixes already-read bytes back onto the original body while
// preserving its Close.
type replayBody struct {
	io.Reader
	io.Closer
}

// writeToolCall classifies a raw streamable POST body: it returns the
// JSON-RPC id with ok true only when the body is a single tools/call request
// for a tool that requires the write scope. Anything else — batch arrays,
// other methods, unknown tools, malformed JSON — is not this gate's business.
func writeToolCall(body []byte) (id json.RawMessage, ok bool) {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, false
	}
	if msg.Method != "tools/call" || !mcpserver.RequiresWriteScope(msg.Params.Name) {
		return nil, false
	}
	return msg.ID, true
}

// writeInsufficientScope emits the RFC 6750 §3.1 refusal: 403, a Bearer
// challenge naming error="insufficient_scope" and the scope to step up to,
// and a JSON-RPC error body whose message is byte-identical to the in-band
// tool-error refusal — go-sdk's streamable client surfaces a JSON-RPC error
// found in a non-2xx body verbatim (mcp/streamable.go checkResponse), so a
// non-step-up agent still reads the full remediation. granted is grant
// vocabulary from the verifier, never credential bytes.
func writeInsufficientScope(w http.ResponseWriter, resourceMetadataURL string, id json.RawMessage, granted []string) {
	params := []string{`error="insufficient_scope"`, `scope="write"`}
	if resourceMetadataURL != "" {
		params = append(params, fmt.Sprintf("resource_metadata=%q", resourceMetadataURL))
	}
	w.Header().Set("WWW-Authenticate", "Bearer "+strings.Join(params, ", "))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)

	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	body := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{JSONRPC: "2.0", ID: id}
	body.Error.Code = insufficientScopeCode
	body.Error.Message = mcpserver.WriteScopeRefusalText(granted)
	_ = json.NewEncoder(w).Encode(body)
}
