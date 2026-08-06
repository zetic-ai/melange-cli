package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/itchyny/gojq"
)

// CheckResult is one scored assertion in the scorecard.
type CheckResult struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// oracleFn runs `melange <args>` against the backend with the write PAT and
// returns its stdout. It is the CLI-as-independent-oracle seam: check
// evaluation never asks the MCP server about state the MCP server produced.
type oracleFn func(ctx context.Context, args []string) ([]byte, error)

// judgeFn scores a rubric against the agent's final answer and tool-call
// sequence, returning the verdict and its reason.
type judgeFn func(ctx context.Context, task Task, rubric string, tr Transcript) (bool, string, error)

// evalChecks scores every check of a task against the transcript and the
// backend. A check that errors (oracle unreachable, bad jq, judge failure)
// is recorded as a failed check with the error as detail — it never aborts
// the task, let alone the suite.
func evalChecks(ctx context.Context, task Task, tr Transcript, oracle oracleFn, judge judgeFn) []CheckResult {
	results := make([]CheckResult, 0, len(task.Checks))
	for _, c := range task.Checks {
		res := CheckResult{Type: c.Type, Name: c.label()}
		switch c.Type {
		case "tool_called":
			res.Pass, res.Detail = evalToolCalled(c, tr)
		case "tool_not_called":
			res.Pass, res.Detail = evalToolNotCalled(c, tr)
		case "jq_state":
			res.Pass, res.Detail = evalJQState(ctx, c, oracle)
		case "judge":
			pass, reason, err := judge(ctx, task, c.Rubric, tr)
			if err != nil {
				res.Pass, res.Detail = false, "judge error: "+err.Error()
			} else {
				res.Pass, res.Detail = pass, reason
			}
		default:
			res.Detail = "unknown check type" // unreachable: loadTasks validated
		}
		results = append(results, res)
	}
	return results
}

// evalToolCalled asserts the transcript contains calls to c.Tool satisfying
// every constraint the check declares.
func evalToolCalled(c Check, tr Transcript) (bool, string) {
	matches := callsNamed(tr, c.Tool)
	if len(matches) == 0 {
		return false, fmt.Sprintf("%s was never called", c.Tool)
	}
	minCalls := c.MinCalls
	if minCalls == 0 {
		minCalls = 1
	}
	if len(matches) < minCalls {
		return false, fmt.Sprintf("%s called %d time(s), want at least %d", c.Tool, len(matches), minCalls)
	}
	if c.MaxCalls > 0 && len(matches) > c.MaxCalls {
		return false, fmt.Sprintf("%s called %d time(s), want at most %d", c.Tool, len(matches), c.MaxCalls)
	}
	if c.InputJQ != "" {
		hit := false
		for _, call := range matches {
			ok, err := jqTruthy(c.InputJQ, call.Input)
			if err != nil {
				return false, fmt.Sprintf("input_jq error: %v", err)
			}
			if ok {
				hit = true
				break
			}
		}
		if !hit {
			return false, fmt.Sprintf("no %s call has an input matching %q", c.Tool, c.InputJQ)
		}
	}
	if c.ResultContains != "" {
		hit := false
		for _, call := range matches {
			if strings.Contains(call.Result, c.ResultContains) {
				hit = true
				break
			}
		}
		if !hit {
			return false, fmt.Sprintf("no %s result contains %q", c.Tool, c.ResultContains)
		}
	}
	return true, fmt.Sprintf("%s called %d time(s)", c.Tool, len(matches))
}

// evalToolNotCalled asserts c.Tool never appears in the transcript.
func evalToolNotCalled(c Check, tr Transcript) (bool, string) {
	matches := callsNamed(tr, c.Tool)
	if len(matches) > 0 {
		return false, fmt.Sprintf("%s was called %d time(s)", c.Tool, len(matches))
	}
	return true, c.Tool + " was never called"
}

// evalJQState runs the oracle CLI command and asserts the jq expression is
// truthy on its stdout JSON.
func evalJQState(ctx context.Context, c Check, oracle oracleFn) (bool, string) {
	out, err := oracle(ctx, c.Args)
	if err != nil {
		return false, fmt.Sprintf("oracle `melange %s`: %v", strings.Join(c.Args, " "), err)
	}
	ok, err := jqTruthy(c.JQ, out)
	if err != nil {
		return false, fmt.Sprintf("jq %q: %v", c.JQ, err)
	}
	if !ok {
		return false, fmt.Sprintf("jq %q is not truthy on `melange %s` output", c.JQ, strings.Join(c.Args, " "))
	}
	return true, fmt.Sprintf("jq %q truthy", c.JQ)
}

// callsNamed returns the transcript's calls to one tool (prefix-stripped
// name).
func callsNamed(tr Transcript, tool string) []ToolCall {
	var out []ToolCall
	for _, call := range tr.ToolCalls {
		if call.Name == tool {
			out = append(out, call)
		}
	}
	return out
}

// jqTruthy evaluates a jq expression against a JSON document and reports
// whether any produced value is truthy (jq semantics: everything except
// false and null). An empty document is evaluated as null.
func jqTruthy(expr string, doc []byte) (bool, error) {
	q, err := gojq.Parse(expr)
	if err != nil {
		return false, fmt.Errorf("parse: %w", err)
	}
	var input any
	if len(doc) > 0 {
		if err := json.Unmarshal(doc, &input); err != nil {
			return false, fmt.Errorf("input is not JSON: %w", err)
		}
	}
	iter := q.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			return false, nil
		}
		if err, isErr := v.(error); isErr {
			return false, err
		}
		if v != nil && v != false {
			return true, nil
		}
	}
}
