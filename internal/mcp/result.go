package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxTextMirrorBytes caps the TextContent mirror of a success payload.
// StructuredContent always carries the full response regardless.
const maxTextMirrorBytes = 16 * 1024

// rawResult wraps a 2xx API response body as a tool result: the exact bytes
// go in StructuredContent (json.RawMessage — never re-marshaled through typed
// structs) with a TextContent mirror for clients that ignore structured
// output. Oversized mirrors are truncated with a notice pointing at
// structuredContent.
func rawResult(body []byte) *mcp.CallToolResult {
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
	}
}
