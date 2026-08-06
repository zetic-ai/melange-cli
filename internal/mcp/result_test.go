package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRawResultPassthrough(t *testing.T) {
	// Non-alphabetical key order: byte equality proves no re-marshal.
	body := []byte(`{"user":{"email":"dev@zetic.ai"},"account":{"name":"zetic"}}`)

	res := rawResult(body)

	assert.False(t, res.IsError)
	raw, ok := res.StructuredContent.(json.RawMessage)
	require.True(t, ok, "StructuredContent must be json.RawMessage, never a typed struct")
	assert.Equal(t, body, []byte(raw), "StructuredContent carries the exact response bytes")
	assert.Equal(t, string(body), textOf(t, res), "text mirror matches the body")
}

func TestRawResultTrimsTrailingWhitespaceInMirrorOnly(t *testing.T) {
	body := []byte("{\"count\":0}\n \t\r\n")

	res := rawResult(body)

	assert.Equal(t, `{"count":0}`, textOf(t, res),
		"mirror trims trailing JSON whitespace like Exporter.Write")
	assert.Equal(t, body, []byte(res.StructuredContent.(json.RawMessage)),
		"StructuredContent stays untouched")
}

func TestRawResultAtCapIsNotTruncated(t *testing.T) {
	// A JSON string of exactly 16384 bytes.
	body := []byte(`"` + strings.Repeat("x", maxTextMirrorBytes-2) + `"`)
	require.Len(t, body, maxTextMirrorBytes)

	res := rawResult(body)

	assert.Equal(t, string(body), textOf(t, res))
	assert.NotContains(t, textOf(t, res), "truncated")
}

func TestRawResultOverCapTruncatesMirror(t *testing.T) {
	body := []byte(`"` + strings.Repeat("x", 19998) + `"`)
	require.Len(t, body, 20000)

	res := rawResult(body)

	text := textOf(t, res)
	notice := "[response truncated: 20000 bytes; full data in structuredContent. Narrow the query or paginate.]"
	assert.True(t, strings.HasPrefix(text, notice), "mirror starts with the truncation notice: %q", text[:120])
	assert.Equal(t, notice+"\n"+string(body[:maxTextMirrorBytes]), text,
		"notice is followed by the first 16 KiB")
	assert.Equal(t, body, []byte(res.StructuredContent.(json.RawMessage)),
		"the full payload survives in StructuredContent")
}

func TestRawResultCapAppliesAfterTrailingWhitespaceTrim(t *testing.T) {
	payload := `"` + strings.Repeat("x", maxTextMirrorBytes-2) + `"`
	body := []byte(payload + "\n")
	require.Len(t, body, maxTextMirrorBytes+1)

	res := rawResult(body)

	assert.Equal(t, payload, textOf(t, res),
		"a payload only over the cap due to trailing whitespace is not truncated")
}

func TestRawResultTruncationNoticeCountsFullBodyBytes(t *testing.T) {
	body := []byte(`"` + strings.Repeat("y", 30000) + `"`)
	res := rawResult(body)
	assert.Contains(t, textOf(t, res), fmt.Sprintf("[response truncated: %d bytes;", len(body)))
}
