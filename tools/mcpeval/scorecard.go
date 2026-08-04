package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Scorecard is the machine-diffable product of one eval run. Field order and
// task ordering are deterministic so two runs diff cleanly — a tool
// description regression is meant to show up as a score delta here.
type Scorecard struct {
	Run   RunInfo      `json:"run"`
	Total Totals       `json:"totals"`
	Tasks []TaskResult `json:"tasks"`
}

// RunInfo records what produced the scores; comparing two scorecards is only
// meaningful when these match.
type RunInfo struct {
	StartedAt     string `json:"started_at"`
	MelangeHost   string `json:"melange_host"`
	Account       string `json:"account"`
	AgentModel    string `json:"agent_model"`
	JudgeModel    string `json:"judge_model"`
	ClaudeVersion string `json:"claude_version,omitempty"`
	// SchemaShim records that the runner interposed the outputSchema repair
	// (see shim.go) — true until the generator defect is fixed.
	SchemaShim bool `json:"schema_shim"`
}

// Totals summarize the run for a one-line diff.
type Totals struct {
	Tasks        int     `json:"tasks"`
	PassedTasks  int     `json:"passed_tasks"`
	SkippedTasks int     `json:"skipped_tasks"`
	Checks       int     `json:"checks"`
	PassedChecks int     `json:"passed_checks"`
	Score        float64 `json:"score"`
	CostUSD      float64 `json:"cost_usd"`
	DurationS    float64 `json:"duration_s"`
}

// TaskResult is one task's outcome: its score, every check verdict, and the
// tool-call sequence that produced them (the sequence is what makes a
// description regression diagnosable rather than just visible).
type TaskResult struct {
	Name    string  `json:"name"`
	Status  string  `json:"status"` // pass | partial | fail | skipped | error
	Score   float64 `json:"score"`
	Skipped string  `json:"skipped_reason,omitempty"`
	// Error carries a harness-level failure (spawn error, timeout note);
	// checks below were still evaluated against whatever transcript exists.
	Error         string          `json:"error,omitempty"`
	ResultSubtype string          `json:"result_subtype,omitempty"`
	NumTurns      int             `json:"num_turns,omitempty"`
	CostUSD       float64         `json:"cost_usd,omitempty"`
	DurationS     float64         `json:"duration_s,omitempty"`
	ToolCalls     []ToolCallEntry `json:"tool_calls"`
	Checks        []CheckResult   `json:"checks"`
	FinalAnswer   string          `json:"final_answer_excerpt,omitempty"`
}

// ToolCallEntry is the scorecard rendering of one transcript tool call.
type ToolCallEntry struct {
	Name    string `json:"name"`
	Input   string `json:"input,omitempty"`
	Result  string `json:"result_excerpt,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// buildTaskResult folds a transcript and its check verdicts into the
// scorecard shape.
func buildTaskResult(task Task, tr Transcript, checks []CheckResult, harnessErr string) TaskResult {
	res := TaskResult{
		Name:          task.Name,
		Error:         harnessErr,
		ResultSubtype: tr.ResultSubtype,
		NumTurns:      tr.NumTurns,
		CostUSD:       tr.CostUSD,
		DurationS:     float64(tr.DurationMS) / 1000,
		Checks:        checks,
		FinalAnswer:   excerpt(tr.FinalAnswer, 600),
		ToolCalls:     make([]ToolCallEntry, 0, len(tr.ToolCalls)),
	}
	for _, call := range tr.ToolCalls {
		res.ToolCalls = append(res.ToolCalls, ToolCallEntry{
			Name:    call.Name,
			Input:   excerpt(string(call.Input), 400),
			Result:  excerpt(call.Result, 200),
			IsError: call.IsError,
		})
	}
	passed := 0
	for _, c := range checks {
		if c.Pass {
			passed++
		}
	}
	if len(checks) > 0 {
		res.Score = round2(float64(passed) / float64(len(checks)))
	}
	switch {
	case harnessErr != "" && len(tr.ToolCalls) == 0 && passed == 0:
		res.Status = "error"
	case passed == len(checks):
		res.Status = "pass"
	case passed == 0:
		res.Status = "fail"
	default:
		res.Status = "partial"
	}
	return res
}

// skippedTaskResult marks a task the runner could not attempt (e.g. no
// read-only PAT for a read-credential task) without failing the suite.
func skippedTaskResult(task Task, reason string) TaskResult {
	return TaskResult{
		Name:      task.Name,
		Status:    "skipped",
		Skipped:   reason,
		ToolCalls: []ToolCallEntry{},
		Checks:    []CheckResult{},
	}
}

// buildScorecard assembles the final scorecard with run totals.
func buildScorecard(run RunInfo, tasks []TaskResult, wallClock time.Duration) Scorecard {
	var t Totals
	t.Tasks = len(tasks)
	var scoreSum float64
	scored := 0
	for _, task := range tasks {
		switch task.Status {
		case "skipped":
			t.SkippedTasks++
			continue
		case "pass":
			t.PassedTasks++
		}
		scored++
		scoreSum += task.Score
		t.CostUSD += task.CostUSD
		for _, c := range task.Checks {
			t.Checks++
			if c.Pass {
				t.PassedChecks++
			}
		}
	}
	if scored > 0 {
		t.Score = round2(scoreSum / float64(scored))
	}
	t.CostUSD = round4(t.CostUSD)
	t.DurationS = round2(wallClock.Seconds())
	return Scorecard{Run: run, Total: t, Tasks: tasks}
}

// writeJSON emits the scorecard as indented JSON with a trailing newline.
func (s Scorecard) writeJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// writeTable renders the human summary: one row per task, then one row per
// check of any non-passing task so the delta is readable without opening the
// JSON.
func (s Scorecard) writeTable(w io.Writer) {
	fmt.Fprintf(w, "\n== mcpeval scorecard (%s, agent=%s judge=%s) ==\n",
		s.Run.StartedAt, s.Run.AgentModel, s.Run.JudgeModel)
	tw := tabwriter.NewWriter(w, 2, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tSTATUS\tSCORE\tTURNS\tTOOL CALLS\tDURATION")
	for _, task := range s.Tasks {
		if task.Status == "skipped" {
			fmt.Fprintf(tw, "%s\tSKIPPED\t-\t-\t-\t- (%s)\n", task.Name, task.Skipped)
			continue
		}
		names := make([]string, 0, len(task.ToolCalls))
		for _, c := range task.ToolCalls {
			names = append(names, c.Name)
		}
		fmt.Fprintf(tw, "%s\t%s\t%.2f\t%d\t%s\t%.0fs\n",
			task.Name, strings.ToUpper(task.Status), task.Score, task.NumTurns,
			compressSequence(names), task.DurationS)
	}
	_ = tw.Flush()
	for _, task := range s.Tasks {
		if task.Status == "pass" || task.Status == "skipped" {
			continue
		}
		fmt.Fprintf(w, "\n%s:\n", task.Name)
		if task.Error != "" {
			fmt.Fprintf(w, "  ERROR %s\n", task.Error)
		}
		for _, c := range task.Checks {
			mark := "PASS"
			if !c.Pass {
				mark = "FAIL"
			}
			fmt.Fprintf(w, "  %s  %s — %s\n", mark, c.Name, c.Detail)
		}
	}
	fmt.Fprintf(w, "\nTOTAL: %d/%d tasks pass (%d skipped), %d/%d checks, score %.2f, cost $%.4f, %.0fs\n",
		s.Total.PassedTasks, s.Total.Tasks-s.Total.SkippedTasks, s.Total.SkippedTasks,
		s.Total.PassedChecks, s.Total.Checks, s.Total.Score, s.Total.CostUSD, s.Total.DurationS)
}

// compressSequence renders a tool-call sequence compactly, folding immediate
// repeats: [a b b b c] -> "a, b x3, c".
func compressSequence(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	var parts []string
	for i := 0; i < len(names); {
		j := i
		for j < len(names) && names[j] == names[i] {
			j++
		}
		if n := j - i; n > 1 {
			parts = append(parts, fmt.Sprintf("%s x%d", names[i], n))
		} else {
			parts = append(parts, names[i])
		}
		i = j
	}
	return strings.Join(parts, ", ")
}

// excerpt clips s to n runes, marking the cut.
func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
func round4(f float64) float64 { return float64(int(f*10000+0.5)) / 10000 }
