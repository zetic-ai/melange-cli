package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// ToolCall is one tool invocation observed in the agent transcript, paired
// with its result once the matching tool_result arrives.
type ToolCall struct {
	// Name has the MCP prefix stripped: "mcp__melange__import_model" is
	// recorded as "import_model" so checks and the scorecard speak the
	// server's own tool vocabulary.
	Name string `json:"name"`
	// RawName preserves the transcript spelling (prefix included).
	RawName string          `json:"raw_name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	// Result is the tool result's concatenated text content.
	Result  string `json:"-"`
	IsError bool   `json:"is_error,omitempty"`

	id string
}

// Transcript is what the runner extracts from a `claude -p --output-format
// stream-json` session: the ordered tool calls, the final answer, and the
// run accounting the result event reports.
type Transcript struct {
	ToolCalls   []ToolCall
	FinalAnswer string
	// ResultSubtype is the result event's subtype ("success",
	// "error_max_turns", ...); empty when no result event arrived (crash or
	// timeout).
	ResultSubtype string
	IsError       bool
	NumTurns      int
	CostUSD       float64
	DurationMS    int64
}

// streamEvent is the envelope shape of one stream-json line. Only the fields
// the runner consumes are declared; unknown event types are skipped.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Result       json.RawMessage `json:"result"`
	IsError      bool            `json:"is_error"`
	NumTurns     int             `json:"num_turns"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	DurationMS   int64           `json:"duration_ms"`
}

// contentBlock is one entry of an assistant/user message content array.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// parseTranscript consumes the stream-json event lines of one agent session.
// It is deliberately forgiving: unknown event types, blank lines, and
// non-JSON noise are skipped, and a truncated stream (timeout, crash) still
// yields every tool call seen so far — checks must be evaluable from a
// partial transcript.
func parseTranscript(r io.Reader) Transcript {
	var t Transcript
	pending := map[string]int{} // tool_use id -> index in t.ToolCalls
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 32*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, b := range parseBlocks(ev.Message.Content) {
				if b.Type != "tool_use" {
					continue
				}
				call := ToolCall{
					Name:    strings.TrimPrefix(b.Name, "mcp__melange__"),
					RawName: b.Name,
					Input:   b.Input,
					id:      b.ID,
				}
				pending[b.ID] = len(t.ToolCalls)
				t.ToolCalls = append(t.ToolCalls, call)
			}
		case "user":
			for _, b := range parseBlocks(ev.Message.Content) {
				if b.Type != "tool_result" {
					continue
				}
				idx, ok := pending[b.ToolUseID]
				if !ok {
					continue
				}
				t.ToolCalls[idx].Result = blockText(b.Content)
				t.ToolCalls[idx].IsError = b.IsError
			}
		case "result":
			t.ResultSubtype = ev.Subtype
			t.IsError = ev.IsError
			t.NumTurns = ev.NumTurns
			t.CostUSD = ev.TotalCostUSD
			t.DurationMS = ev.DurationMS
			var answer string
			if err := json.Unmarshal(ev.Result, &answer); err == nil {
				t.FinalAnswer = answer
			} else if len(ev.Result) > 0 {
				t.FinalAnswer = string(ev.Result)
			}
		}
	}
	return t
}

// parseBlocks decodes a message content field that is either a block array
// or a bare string.
func parseBlocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []contentBlock{{Type: "text", Text: s}}
	}
	return nil
}

// blockText flattens a tool_result content value — a string, or an array of
// typed blocks — into one text blob for substring checks and excerpts.
func blockText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return string(raw)
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
