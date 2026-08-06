package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func call(name, input, result string, isErr bool) ToolCall {
	return ToolCall{Name: name, Input: json.RawMessage(input), Result: result, IsError: isErr}
}

func TestEvalToolCalled(t *testing.T) {
	tr := Transcript{ToolCalls: []ToolCall{
		call("delete_repo", `{"repo":"a/x"}`, "confirm missing", true),
		call("delete_repo", `{"repo":"a/x","confirm":"a/x"}`, "deleted", false),
		call("whoami", `{}`, "ok", false),
	}}

	cases := []struct {
		name     string
		check    Check
		wantPass bool
	}{
		{"present", Check{Type: "tool_called", Tool: "whoami"}, true},
		{"absent", Check{Type: "tool_called", Tool: "import_model"}, false},
		{"min met", Check{Type: "tool_called", Tool: "delete_repo", MinCalls: 2}, true},
		{"min unmet", Check{Type: "tool_called", Tool: "delete_repo", MinCalls: 3}, false},
		{"max met", Check{Type: "tool_called", Tool: "whoami", MaxCalls: 1}, true},
		{"max exceeded", Check{Type: "tool_called", Tool: "delete_repo", MaxCalls: 1}, false},
		{"input_jq matches one call", Check{Type: "tool_called", Tool: "delete_repo", InputJQ: ".confirm == .repo"}, true},
		{"input_jq matches none", Check{Type: "tool_called", Tool: "whoami", InputJQ: `.confirm == "x"`}, false},
		{"input_jq invalid", Check{Type: "tool_called", Tool: "whoami", InputJQ: "((("}, false},
		{"result_contains hit", Check{Type: "tool_called", Tool: "delete_repo", ResultContains: "deleted"}, true},
		{"result_contains miss", Check{Type: "tool_called", Tool: "delete_repo", ResultContains: "insufficient scope"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass, detail := evalToolCalled(tc.check, tr)
			assert.Equal(t, tc.wantPass, pass, detail)
			assert.NotEmpty(t, detail, "every verdict needs a diagnosable detail")
		})
	}
}

func TestEvalToolNotCalled(t *testing.T) {
	tr := Transcript{ToolCalls: []ToolCall{call("import_model", `{}`, "", false)}}
	pass, detail := evalToolNotCalled(Check{Type: "tool_not_called", Tool: "import_model"}, tr)
	assert.False(t, pass)
	assert.Contains(t, detail, "1 time")
	pass, _ = evalToolNotCalled(Check{Type: "tool_not_called", Tool: "delete_repo"}, tr)
	assert.True(t, pass)
}

func TestEvalJQState(t *testing.T) {
	oracle := func(_ context.Context, args []string) ([]byte, error) {
		switch args[0] {
		case "boom":
			return nil, errors.New("backend unreachable")
		case "notjson":
			return []byte("plain text"), nil
		}
		return []byte(`{"results":[{"name":"evaltmp-a"}],"count":1}`), nil
	}
	ctx := context.Background()

	pass, _ := evalJQState(ctx, Check{Args: []string{"repo", "list"}, JQ: ".count == 1"}, oracle)
	assert.True(t, pass)

	pass, detail := evalJQState(ctx, Check{Args: []string{"repo", "list"}, JQ: ".count == 2"}, oracle)
	assert.False(t, pass)
	assert.Contains(t, detail, "not truthy")

	pass, detail = evalJQState(ctx, Check{Args: []string{"boom"}, JQ: "."}, oracle)
	assert.False(t, pass, "an oracle error is a failed check, not a crash")
	assert.Contains(t, detail, "backend unreachable")

	pass, detail = evalJQState(ctx, Check{Args: []string{"notjson"}, JQ: "."}, oracle)
	assert.False(t, pass)
	assert.Contains(t, detail, "not JSON")

	pass, detail = evalJQState(ctx, Check{Args: []string{"repo", "list"}, JQ: "((("}, oracle)
	assert.False(t, pass)
	assert.Contains(t, detail, "parse")
}

func TestJQTruthySemantics(t *testing.T) {
	// jq truthiness: false and null are falsy, everything else truthy —
	// including 0 and "". A check like `.count >= 1` depends on this.
	for expr, want := range map[string]bool{
		"true": true, "false": false, "null": false,
		"0": true, `""`: true, "[] | length == 0": true,
		".missing":                              false, // null
		`.results[]? | select(.name == "nope")`: false, // empty stream
	} {
		got, err := jqTruthy(expr, []byte(`{"results":[]}`))
		require.NoError(t, err, expr)
		assert.Equal(t, want, got, expr)
	}
}

func TestEvalChecksJudgeAndOrdering(t *testing.T) {
	task := Task{
		Name:   "t",
		Prompt: "p",
		Checks: []Check{
			{Type: "tool_called", Tool: "whoami"},
			{Type: "judge", Rubric: "was it good?"},
			{Type: "judge", Rubric: "explode"},
		},
	}
	tr := Transcript{ToolCalls: []ToolCall{call("whoami", `{}`, "ok", false)}}
	judge := func(_ context.Context, _ Task, rubric string, _ Transcript) (bool, string, error) {
		if rubric == "explode" {
			return false, "", fmt.Errorf("api down")
		}
		return true, "solid work", nil
	}
	results := evalChecks(context.Background(), task, tr, nil, judge)
	require.Len(t, results, 3)
	assert.True(t, results[0].Pass)
	assert.True(t, results[1].Pass)
	assert.Equal(t, "solid work", results[1].Detail, "the judge's reason must reach the scorecard")
	assert.False(t, results[2].Pass, "a judge error is a failed check, not a crash")
	assert.Contains(t, results[2].Detail, "api down")
}

func TestParseJudgeVerdict(t *testing.T) {
	pass, reason, err := parseJudgeVerdict([]byte(`{"subtype":"success","result":"{\"pass\":true,\"reason\":\"did the thing\"}"}`))
	require.NoError(t, err)
	assert.True(t, pass)
	assert.Equal(t, "did the thing", reason)

	// Fenced output still yields the verdict.
	pass, _, err = parseJudgeVerdict([]byte(`{"result":"` + "```json\\n" + `{\"pass\":false,\"reason\":\"nope\"}` + "\\n```" + `"}`))
	require.NoError(t, err)
	assert.False(t, pass)

	_, _, err = parseJudgeVerdict([]byte(`{"is_error":true,"subtype":"error_during_execution","result":""}`))
	require.Error(t, err)

	_, _, err = parseJudgeVerdict([]byte(`not json`))
	require.Error(t, err)

	_, _, err = parseJudgeVerdict([]byte(`{"result":"no verdict here"}`))
	require.Error(t, err)
}

func TestBuildJudgePromptCarriesEvidence(t *testing.T) {
	task := Task{Prompt: "Delete every evaltmp- repo."}
	tr := Transcript{
		ToolCalls:   []ToolCall{call("delete_repo", `{"repo":"a/evaltmp-x","confirm":"a/evaltmp-x"}`, "deleted", false)},
		FinalAnswer: "Deleted a/evaltmp-x.",
	}
	p := buildJudgePrompt(task, "the rubric text", tr)
	assert.Contains(t, p, "Delete every evaltmp- repo.")
	assert.Contains(t, p, "delete_repo")
	assert.Contains(t, p, `"confirm":"a/evaltmp-x"`)
	assert.Contains(t, p, "Deleted a/evaltmp-x.")
	assert.Contains(t, p, "the rubric text")

	empty := buildJudgePrompt(task, "r", Transcript{})
	assert.Contains(t, empty, "(none)", "an empty transcript must be explicit, not blank")
	assert.Contains(t, empty, "(no final answer was produced)")
}
