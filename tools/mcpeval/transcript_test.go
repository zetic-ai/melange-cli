package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureStream mirrors the event shapes `claude -p --output-format
// stream-json --verbose` emits (verified against claude 2.1.220): system
// noise, tool_use blocks, tool_result as an array of text blocks AND as a
// bare string, an error result, and the final result event.
const fixtureStream = `{"type":"system","subtype":"init","tools":[],"mcp_servers":[{"name":"melange","status":"pending"}],"model":"claude-x"}
{"type":"rate_limit_event"}
not json noise
{"type":"assistant","message":{"content":[{"type":"thinking"},{"type":"tool_use","id":"t1","name":"mcp__melange__search_library","input":{"search":"whisper"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"{\"count\":1,\"results\":[{\"full_name\":\"acme/whisper\"}]}"}]}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"mcp__melange__delete_repo","input":{"repo":"acme/x","confirm":"acme/x"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"plain string result","is_error":true}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"All done."}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.0421,"duration_ms":15250,"result":"I found acme/whisper and deleted acme/x."}
`

func TestParseTranscriptFullSession(t *testing.T) {
	tr := parseTranscript(strings.NewReader(fixtureStream))

	require.Len(t, tr.ToolCalls, 2)
	assert.Equal(t, "search_library", tr.ToolCalls[0].Name, "the mcp__melange__ prefix must be stripped")
	assert.Equal(t, "mcp__melange__search_library", tr.ToolCalls[0].RawName)
	assert.JSONEq(t, `{"search":"whisper"}`, string(tr.ToolCalls[0].Input))
	assert.Contains(t, tr.ToolCalls[0].Result, `"full_name":"acme/whisper"`, "array-of-blocks results must be flattened to text")
	assert.False(t, tr.ToolCalls[0].IsError)

	assert.Equal(t, "delete_repo", tr.ToolCalls[1].Name)
	assert.Equal(t, "plain string result", tr.ToolCalls[1].Result, "bare-string results must parse too")
	assert.True(t, tr.ToolCalls[1].IsError)

	assert.Equal(t, "I found acme/whisper and deleted acme/x.", tr.FinalAnswer)
	assert.Equal(t, "success", tr.ResultSubtype)
	assert.Equal(t, 3, tr.NumTurns)
	assert.InDelta(t, 0.0421, tr.CostUSD, 1e-9)
	assert.EqualValues(t, 15250, tr.DurationMS)
}

func TestParseTranscriptPartialStream(t *testing.T) {
	// A timeout truncates the stream after a tool call: the call must still
	// be visible so checks can score the partial run.
	partial := strings.Join(strings.Split(fixtureStream, "\n")[:5], "\n")
	tr := parseTranscript(strings.NewReader(partial))
	require.Len(t, tr.ToolCalls, 1)
	assert.Equal(t, "search_library", tr.ToolCalls[0].Name)
	assert.Empty(t, tr.FinalAnswer, "no result event means no final answer")
	assert.Empty(t, tr.ResultSubtype)
}

func TestParseTranscriptEmptyAndGarbage(t *testing.T) {
	assert.Empty(t, parseTranscript(strings.NewReader("")).ToolCalls)
	assert.Empty(t, parseTranscript(strings.NewReader("garbage\n{broken json\n")).ToolCalls)
}

func TestParseTranscriptUnmatchedToolResultIgnored(t *testing.T) {
	tr := parseTranscript(strings.NewReader(
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"ghost","content":"x"}]}}` + "\n"))
	assert.Empty(t, tr.ToolCalls, "a result without its call must not invent a call")
}
