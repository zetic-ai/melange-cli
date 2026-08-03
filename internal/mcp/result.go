package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxTextMirrorBytes caps the TextContent mirror of a success payload.
// StructuredContent always carries the full response regardless.
const maxTextMirrorBytes = 16 * 1024

// marshalEnvelope renders a composite tool envelope whose fields are raw API
// response halves.
//
// It exists because json.Marshal HTML-escapes what it re-emits: a half
// containing `readme: "# a <img> & b"` would reach the caller as `<img>
// &`, silently rewriting the very bytes json.RawMessage halves exist to
// preserve. An Encoder with SetEscapeHTML(false) keeps them intact. Only
// insignificant JSON whitespace between tokens is normalized; no value,
// key, or key order is touched.
func marshalEnvelope(envelope any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(envelope); err != nil {
		return nil, err
	}
	// Encode appends a newline; the halves' own bytes end where they end.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// errEmptyBody reports a success path that reached rawResult with no bytes.
// It is a programming fault (Go-error class, not a toolError): every 2xx
// exchange these tools wrap carries a JSON body, so an empty one means a
// handler wired the wrong bytes through. The class became reachable with the
// upload-session operations, hence the explicit guard instead of silently
// framing an empty success payload.
var errEmptyBody = errors.New("mcp: empty response body on a successful tool call (programming fault)")

// rawResult wraps a 2xx API response body as a tool result: the exact bytes
// go in StructuredContent (json.RawMessage — never re-marshaled through typed
// structs) with a TextContent mirror for clients that ignore structured
// output. Oversized mirrors are truncated with a notice pointing at
// structuredContent.
//
// It returns the full handler triple so passthrough handlers stay one-liners;
// the Out value is always nil (byte-exact passthrough never uses typed
// output). An empty body returns errEmptyBody instead of a result.
func rawResult(body []byte) (*mcp.CallToolResult, any, error) {
	if len(body) == 0 {
		return nil, nil, errEmptyBody
	}
	// Mirror Exporter.Write: trim trailing JSON whitespace (RFC 8259) so the
	// text mirror never ends in a dangling newline.
	trimmed := bytes.TrimRight(body, " \t\r\n")
	mirror := string(trimmed)
	if len(trimmed) > maxTextMirrorBytes {
		mirror = fmt.Sprintf(
			"[response truncated: %d bytes; full data in structuredContent. Narrow the query or paginate.]\n%s",
			len(body), trimmed[:maxTextMirrorBytes])
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: mirror}},
		StructuredContent: json.RawMessage(body),
	}, nil, nil
}
