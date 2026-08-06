package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Task is one agent eval scenario, loaded from tools/mcpeval/tasks/*.yaml.
// Placeholders of the form ${ACCOUNT} and ${HF_REPO} are expanded in the
// prompt and in setup/cleanup/check argument lists before the task runs.
type Task struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Transport selects how the agent reaches the MCP server: "stdio" (the
	// melange binary spawned by the agent client) or "http" (a runner-managed
	// `melange mcp --transport http --validate-tokens` instance).
	Transport string `yaml:"transport"`
	// Credential selects which seeded PAT the MCP server sees: "write" (the
	// default) or "read" (the read-only PAT, for scope tasks).
	Credential     string    `yaml:"credential"`
	Prompt         string    `yaml:"prompt"`
	MaxTurns       int       `yaml:"max_turns"`
	TimeoutSeconds int       `yaml:"timeout_seconds"`
	Setup          []CLIStep `yaml:"setup"`
	Cleanup        []CLIStep `yaml:"cleanup"`
	Checks         []Check   `yaml:"checks"`
}

// CLIStep is one oracle-side `melange` invocation run before or after the
// agent (seeding or removing backend state). Steps never go through the MCP
// server.
type CLIStep struct {
	Args []string `yaml:"args"`
	// MayFail marks steps whose failure is expected and ignored (e.g.
	// deleting a repo that does not exist yet).
	MayFail bool `yaml:"may_fail"`
}

// Check is one scored assertion. Type selects which fields apply:
//
//   - jq_state: run `melange <args>` (the CLI as an independent oracle —
//     never the MCP server) and assert the jq expression is truthy on its
//     stdout JSON.
//   - tool_called: assert the transcript contains calls to Tool — optionally
//     at least MinCalls of them, at most MaxCalls, at least one with an input
//     matching InputJQ, and at least one whose result text contains
//     ResultContains.
//   - tool_not_called: assert Tool never appears in the transcript.
//   - judge: score the agent's final answer against Rubric with an LLM call.
type Check struct {
	Type string `yaml:"type"`
	// Name labels the check in the scorecard; defaults to a description
	// derived from the type.
	Name string `yaml:"name"`

	// tool_called / tool_not_called
	Tool           string `yaml:"tool"`
	MinCalls       int    `yaml:"min_calls"`
	MaxCalls       int    `yaml:"max_calls"`
	InputJQ        string `yaml:"input_jq"`
	ResultContains string `yaml:"result_contains"`

	// jq_state
	Args []string `yaml:"args"`
	JQ   string   `yaml:"jq"`

	// judge
	Rubric string `yaml:"rubric"`
}

// label is the check's scorecard identity.
func (c Check) label() string {
	if c.Name != "" {
		return c.Name
	}
	switch c.Type {
	case "tool_called", "tool_not_called":
		return c.Type + " " + c.Tool
	case "jq_state":
		return "jq_state " + strings.Join(c.Args, " ")
	case "judge":
		return "judge"
	}
	return c.Type
}

// validTransports and validCredentials pin the task vocabulary so a typo in
// a YAML file fails at load time, not mid-run.
var (
	validTransports  = map[string]bool{"stdio": true, "http": true}
	validCredentials = map[string]bool{"write": true, "read": true}
	validCheckTypes  = map[string]bool{"jq_state": true, "tool_called": true, "tool_not_called": true, "judge": true}
)

// validate rejects a structurally unusable task with a message naming the
// exact field.
func (t *Task) validate() error {
	if t.Name == "" {
		return fmt.Errorf("task has no name")
	}
	if t.Prompt == "" {
		return fmt.Errorf("task %q: prompt is empty", t.Name)
	}
	if t.Transport == "" {
		t.Transport = "stdio"
	}
	if !validTransports[t.Transport] {
		return fmt.Errorf("task %q: unknown transport %q (want stdio or http)", t.Name, t.Transport)
	}
	if t.Credential == "" {
		t.Credential = "write"
	}
	if !validCredentials[t.Credential] {
		return fmt.Errorf("task %q: unknown credential %q (want write or read)", t.Name, t.Credential)
	}
	if t.MaxTurns <= 0 {
		return fmt.Errorf("task %q: max_turns must be positive", t.Name)
	}
	if t.TimeoutSeconds <= 0 {
		return fmt.Errorf("task %q: timeout_seconds must be positive", t.Name)
	}
	if len(t.Checks) == 0 {
		return fmt.Errorf("task %q: no checks", t.Name)
	}
	for i, c := range t.Checks {
		if !validCheckTypes[c.Type] {
			return fmt.Errorf("task %q check %d: unknown type %q", t.Name, i, c.Type)
		}
		switch c.Type {
		case "tool_called", "tool_not_called":
			if c.Tool == "" {
				return fmt.Errorf("task %q check %d (%s): tool is required", t.Name, i, c.Type)
			}
		case "jq_state":
			if len(c.Args) == 0 || c.JQ == "" {
				return fmt.Errorf("task %q check %d (jq_state): args and jq are required", t.Name, i)
			}
		case "judge":
			if c.Rubric == "" {
				return fmt.Errorf("task %q check %d (judge): rubric is required", t.Name, i)
			}
		}
	}
	return nil
}

// expand substitutes ${VAR} placeholders in the prompt and every argument
// list. Unknown placeholders are an error: a task must not silently run with
// a literal "${ACCOUNT}" in its prompt.
func (t *Task) expand(vars map[string]string) error {
	var missing []string
	sub := func(s string) string {
		return os.Expand(s, func(name string) string {
			v, ok := vars[name]
			if !ok {
				missing = append(missing, name)
			}
			return v
		})
	}
	t.Prompt = sub(t.Prompt)
	for si := range t.Setup {
		for ai := range t.Setup[si].Args {
			t.Setup[si].Args[ai] = sub(t.Setup[si].Args[ai])
		}
	}
	for si := range t.Cleanup {
		for ai := range t.Cleanup[si].Args {
			t.Cleanup[si].Args[ai] = sub(t.Cleanup[si].Args[ai])
		}
	}
	for ci := range t.Checks {
		for ai := range t.Checks[ci].Args {
			t.Checks[ci].Args[ai] = sub(t.Checks[ci].Args[ai])
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("task %q: unknown placeholder(s): %s", t.Name, strings.Join(missing, ", "))
	}
	return nil
}

// loadTasks reads every *.yaml task in dir, validates it, and returns the
// set sorted by name for a deterministic run (and a diffable scorecard).
func loadTasks(dir string) ([]Task, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no *.yaml tasks in %s", dir)
	}
	sort.Strings(entries)
	tasks := make([]Task, 0, len(entries))
	seen := map[string]string{}
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var t Task
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&t); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err := t.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if prev, dup := seen[t.Name]; dup {
			return nil, fmt.Errorf("%s: duplicate task name %q (also in %s)", path, t.Name, prev)
		}
		seen[t.Name] = path
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Name < tasks[j].Name })
	return tasks, nil
}
