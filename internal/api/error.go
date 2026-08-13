// Package api provides the HTTP client wrapper for the Melange public API:
// transport chain (auth, retry, debug), error-envelope decoding, and the
// hand-written calls that will later be replaced by a generated client.
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// maxErrorBodyBytes bounds how much of an error response body is read.
const maxErrorBodyBytes = 32 * 1024

// FieldError describes a single invalid field in a request.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error is a non-2xx response from the Melange API, decoded from the server
// error envelope when present.
type Error struct {
	StatusCode int
	Type       string // e.g. authentication_error, rate_limit_error, invalid_request_error
	Code       string // machine-readable refusal code, e.g. credit_balance_exhausted; "" when absent
	Message    string
	Fields     []FieldError
	// ActiveUploadID is populated by upload-session conflict responses. It is
	// optional so clients remain compatible with servers that predate the
	// structured conflict field.
	ActiveUploadID string
	RequestID      string        // top-level request_id, or X-Request-ID header fallback
	RetryAfter     time.Duration // parsed Retry-After header, 0 if absent
}

// Error implements the error interface.
func (e *Error) Error() string {
	kind := e.Type
	if e.Code != "" {
		kind += "/" + e.Code
	}
	s := fmt.Sprintf("melange API: %s (%s, HTTP %d", e.Message, kind, e.StatusCode)
	if e.RequestID != "" {
		s += ", request " + e.RequestID
	}
	return s + ")"
}

// Retryable reports whether the request may be safely retried.
func (e *Error) Retryable() bool {
	switch e.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// errorEnvelope mirrors the server error body:
//
//	{"type":"error","error":{"type":"...","message":"...","fields":[...]},"request_id":"..."}
type errorEnvelope struct {
	Type  string `json:"type"`
	Error struct {
		Type           string       `json:"type"`
		Code           string       `json:"code"`
		Message        string       `json:"message"`
		Fields         []FieldError `json:"fields"`
		ActiveUploadID string       `json:"active_upload_id"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// HandleResponse returns nil for 2xx responses (leaving the body untouched);
// otherwise it consumes the body and returns an *Error. Exported so callers
// using Do directly can reuse the envelope decoding.
func HandleResponse(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return ErrorFrom(resp.StatusCode, resp.Header, body)
}

// ErrorFrom converts an already-read response into an error: nil for 2xx,
// otherwise an *Error decoded from the server error envelope (with a
// status-derived fallback for non-envelope bodies). It is the uniform way to
// surface non-2xx results from generated-client responses, which carry the
// body as bytes.
func ErrorFrom(status int, header http.Header, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}

	apiErr := &Error{
		StatusCode: status,
		RequestID:  header.Get("X-Request-ID"),
		RetryAfter: parseRetryAfter(header.Get("Retry-After")),
	}
	if len(body) > maxErrorBodyBytes {
		body = body[:maxErrorBodyBytes]
	}

	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Type == "error" &&
		(env.Error.Type != "" || env.Error.Message != "") {
		apiErr.Type = env.Error.Type
		apiErr.Code = env.Error.Code
		apiErr.Message = env.Error.Message
		apiErr.Fields = env.Error.Fields
		apiErr.ActiveUploadID = env.Error.ActiveUploadID
		if env.RequestID != "" {
			apiErr.RequestID = env.RequestID
		}
		return apiErr
	}

	// Non-envelope body (proxy, load balancer, GCS, ...): derive from status.
	apiErr.Type = fallbackType(status)
	apiErr.Message = fallbackMessage(body, status)
	return apiErr
}

// fallbackType derives an error type from the status code when the body
// carries no envelope.
func fallbackType(status int) string {
	if status == http.StatusUnauthorized {
		return "authentication_error"
	}
	return "http_error"
}

// fallbackMessage is the first 200 bytes of the body (never splitting a
// multibyte UTF-8 rune), or the status text when the body is empty.
func fallbackMessage(body []byte, status int) string {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return http.StatusText(status)
	}
	if len(msg) > 200 {
		cut := 200
		// Walk back to a rune boundary so no partial character survives.
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut]
	}
	return msg
}

// parseRetryAfter parses a Retry-After header value: either delay-seconds or
// an HTTP date. Returns 0 when absent or unparsable.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
