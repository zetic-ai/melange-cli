package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTaskResultStatuses(t *testing.T) {
	task := Task{Name: "t"}
	tr := Transcript{
		ToolCalls:  []ToolCall{call("whoami", `{}`, "ok", false)},
		NumTurns:   4,
		CostUSD:    0.12,
		DurationMS: 8000,
	}
	pass := CheckResult{Pass: true}
	fail := CheckResult{Pass: false}

	assert.Equal(t, "pass", buildTaskResult(task, tr, []CheckResult{pass, pass}, "").Status)
	assert.Equal(t, "partial", buildTaskResult(task, tr, []CheckResult{pass, fail}, "").Status)
	assert.Equal(t, "fail", buildTaskResult(task, tr, []CheckResult{fail, fail}, "").Status)
	assert.Equal(t, "error", buildTaskResult(task, Transcript{}, nil, "spawn failed").Status,
		"a task with no transcript and no passing checks is a harness error")

	// A timeout with a useful partial transcript is scored, not erased.
	timedOut := buildTaskResult(task, tr, []CheckResult{pass, fail}, "task timeout after 60s")
	assert.Equal(t, "partial", timedOut.Status)
	assert.Equal(t, 0.5, timedOut.Score)
	assert.Equal(t, "task timeout after 60s", timedOut.Error)
}

func TestBuildTaskResultScoreAndCalls(t *testing.T) {
	task := Task{Name: "t"}
	tr := Transcript{ToolCalls: []ToolCall{
		call("search_library", `{"search":"whisper"}`, strings.Repeat("x", 500), false),
	}}
	res := buildTaskResult(task, tr, []CheckResult{{Pass: true}, {Pass: false}, {Pass: false}}, "")
	assert.Equal(t, 0.33, res.Score)
	require.Len(t, res.ToolCalls, 1)
	assert.Equal(t, "search_library", res.ToolCalls[0].Name)
	assert.LessOrEqual(t, len(res.ToolCalls[0].Result), 210, "result excerpts must be clipped")
}

func TestBuildScorecardTotals(t *testing.T) {
	tasks := []TaskResult{
		{Name: "a", Status: "pass", Score: 1, CostUSD: 0.10,
			Checks: []CheckResult{{Pass: true}, {Pass: true}}},
		{Name: "b", Status: "partial", Score: 0.5, CostUSD: 0.20,
			Checks: []CheckResult{{Pass: true}, {Pass: false}}},
		{Name: "c", Status: "skipped", Skipped: "no read PAT"},
	}
	card := buildScorecard(RunInfo{AgentModel: "sonnet"}, tasks, 90*time.Second)
	assert.Equal(t, 3, card.Total.Tasks)
	assert.Equal(t, 1, card.Total.PassedTasks)
	assert.Equal(t, 1, card.Total.SkippedTasks)
	assert.Equal(t, 4, card.Total.Checks, "skipped tasks contribute no checks")
	assert.Equal(t, 3, card.Total.PassedChecks)
	assert.Equal(t, 0.75, card.Total.Score, "suite score averages scored tasks only")
	assert.Equal(t, 0.3, card.Total.CostUSD)
	assert.Equal(t, 90.0, card.Total.DurationS)
}

func TestScorecardJSONRoundTrips(t *testing.T) {
	card := buildScorecard(RunInfo{StartedAt: "now", SchemaShim: true},
		[]TaskResult{{Name: "a", Status: "pass", Score: 1, ToolCalls: []ToolCallEntry{}, Checks: []CheckResult{{Type: "judge", Name: "judge", Pass: true}}}},
		time.Second)
	var buf bytes.Buffer
	require.NoError(t, card.writeJSON(&buf))
	var back Scorecard
	require.NoError(t, json.Unmarshal(buf.Bytes(), &back))
	assert.Equal(t, card.Total, back.Total)
	assert.True(t, back.Run.SchemaShim, "the shim must be declared in the scorecard")
}

func TestWriteTableShowsFailingChecks(t *testing.T) {
	card := buildScorecard(RunInfo{StartedAt: "now", AgentModel: "sonnet", JudgeModel: "haiku"},
		[]TaskResult{
			{Name: "good", Status: "pass", Score: 1, NumTurns: 3,
				ToolCalls: []ToolCallEntry{{Name: "whoami"}}},
			{Name: "bad", Status: "partial", Score: 0.5, NumTurns: 5,
				ToolCalls: []ToolCallEntry{{Name: "import_model"}, {Name: "get_conversion_status"}, {Name: "get_conversion_status"}},
				Checks: []CheckResult{
					{Name: "repo exists", Pass: true, Detail: "ok"},
					{Name: "judge", Pass: false, Detail: "fabricated completion"},
				}},
			{Name: "skipped-one", Status: "skipped", Skipped: "no read PAT"},
		}, time.Minute)
	var buf bytes.Buffer
	card.writeTable(&buf)
	out := buf.String()
	assert.Contains(t, out, "good")
	assert.Contains(t, out, "PASS")
	assert.Contains(t, out, "import_model, get_conversion_status x2", "tool sequences must be visible in the table")
	assert.Contains(t, out, "FAIL  judge — fabricated completion", "failing checks must surface without opening the JSON")
	assert.Contains(t, out, "no read PAT")
	assert.NotContains(t, out, "\ngood:\n", "passing tasks get no per-check detail block")
	assert.Contains(t, out, "TOTAL: 1/2 tasks pass (1 skipped)")
}

func TestCompressSequence(t *testing.T) {
	assert.Equal(t, "(none)", compressSequence(nil))
	assert.Equal(t, "a, b x3, a", compressSequence([]string{"a", "b", "b", "b", "a"}))
}

func TestExcerptClipsRunes(t *testing.T) {
	assert.Equal(t, "short", excerpt("  short  ", 10))
	long := excerpt(strings.Repeat("é", 20), 5)
	assert.Equal(t, "ééééé…", long, "clipping must respect rune boundaries")
}
