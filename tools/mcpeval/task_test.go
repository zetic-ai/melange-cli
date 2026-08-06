package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTask(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}

const minimalTask = `
name: sample
prompt: do the thing on ${ACCOUNT}
max_turns: 5
timeout_seconds: 60
checks:
  - type: tool_called
    tool: whoami
`

func TestLoadTasksMinimalDefaults(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "sample.yaml", minimalTask)
	tasks, err := loadTasks(dir)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "stdio", tasks[0].Transport, "transport must default to stdio")
	assert.Equal(t, "write", tasks[0].Credential, "credential must default to write")
}

func TestLoadTasksRejectsBrokenDefinitions(t *testing.T) {
	cases := []struct {
		name, yaml, wantErr string
	}{
		{"unknown check type", strings.Replace(minimalTask, "tool_called", "tool_calld", 1), "unknown type"},
		{"missing prompt", strings.Replace(minimalTask, "prompt: do the thing on ${ACCOUNT}", "prompt: \"\"", 1), "prompt is empty"},
		{"zero max_turns", strings.Replace(minimalTask, "max_turns: 5", "max_turns: 0", 1), "max_turns"},
		{"zero timeout", strings.Replace(minimalTask, "timeout_seconds: 60", "timeout_seconds: 0", 1), "timeout_seconds"},
		{"bad transport", minimalTask + "transport: carrier-pigeon\n", "unknown transport"},
		{"bad credential", minimalTask + "credential: root\n", "unknown credential"},
		{"unknown field", minimalTask + "promt: typo\n", "promt"},
		{"jq_state missing jq", `
name: s
prompt: p
max_turns: 1
timeout_seconds: 1
checks:
  - type: jq_state
    args: [repo, list]
`, "args and jq are required"},
		{"judge missing rubric", `
name: s
prompt: p
max_turns: 1
timeout_seconds: 1
checks:
  - type: judge
`, "rubric is required"},
		{"tool_called missing tool", `
name: s
prompt: p
max_turns: 1
timeout_seconds: 1
checks:
  - type: tool_called
`, "tool is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTask(t, dir, "bad.yaml", tc.yaml)
			_, err := loadTasks(dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoadTasksRejectsDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "a.yaml", minimalTask)
	writeTask(t, dir, "b.yaml", minimalTask)
	_, err := loadTasks(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate task name")
}

func TestLoadTasksSortsByName(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "zz.yaml", strings.Replace(minimalTask, "name: sample", "name: alpha", 1))
	writeTask(t, dir, "aa.yaml", strings.Replace(minimalTask, "name: sample", "name: beta", 1))
	tasks, err := loadTasks(dir)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, "alpha", tasks[0].Name, "tasks must run in name order for a diffable scorecard")
	assert.Equal(t, "beta", tasks[1].Name)
}

func TestExpandSubstitutesEverywhere(t *testing.T) {
	task := Task{
		Name:   "t",
		Prompt: "import into ${ACCOUNT}/repo from ${HF_REPO}",
		Setup:  []CLIStep{{Args: []string{"repo", "delete", "${ACCOUNT}/x"}}},
		Cleanup: []CLIStep{
			{Args: []string{"repo", "delete", "${ACCOUNT}/y"}},
		},
		Checks: []Check{{Type: "jq_state", Args: []string{"repo", "view", "${ACCOUNT}/x"}, JQ: "."}},
	}
	require.NoError(t, task.expand(map[string]string{"ACCOUNT": "acme", "HF_REPO": "org/model"}))
	assert.Equal(t, "import into acme/repo from org/model", task.Prompt)
	assert.Equal(t, "acme/x", task.Setup[0].Args[2])
	assert.Equal(t, "acme/y", task.Cleanup[0].Args[2])
	assert.Equal(t, "acme/x", task.Checks[0].Args[2])
}

func TestExpandRejectsUnknownPlaceholder(t *testing.T) {
	task := Task{Name: "t", Prompt: "hello ${NOPE}"}
	err := task.expand(map[string]string{"ACCOUNT": "a"})
	require.Error(t, err, "a task must not run with a literal placeholder in its prompt")
	assert.Contains(t, err.Error(), "NOPE")
}

// TestSeedTasksAreLoadable pins the committed seed tasks themselves: they
// must parse, validate, expand with the standard variables, and stay four.
func TestSeedTasksAreLoadable(t *testing.T) {
	tasks, err := loadTasks("tasks")
	require.NoError(t, err)
	require.Len(t, tasks, 4, "the four seed tasks are part of the harness contract")
	names := map[string]Task{}
	for i := range tasks {
		require.NoError(t, tasks[i].expand(map[string]string{"ACCOUNT": "acme", "HF_REPO": "org/m"}))
		names[tasks[i].Name] = tasks[i]
	}
	require.Contains(t, names, "import-and-report")
	require.Contains(t, names, "library-to-deploy")
	require.Contains(t, names, "consent-gate")
	require.Contains(t, names, "scope-refusal")

	// The capability each task probes is encoded in its checks; pin the
	// load-bearing ones so an edit cannot silently defang a task.
	assert.Equal(t, "http", names["scope-refusal"].Transport, "scope-refusal must use the HTTP transport (stdio does not enforce scopes)")
	assert.Equal(t, "read", names["scope-refusal"].Credential)
	hasNotCalled := false
	for _, c := range names["library-to-deploy"].Checks {
		if c.Type == "tool_not_called" && c.Tool == "import_model" {
			hasNotCalled = true
		}
	}
	assert.True(t, hasNotCalled, "library-to-deploy must pin that the agent does not import")
	hasConfirm := false
	for _, c := range names["consent-gate"].Checks {
		if c.Type == "tool_called" && c.Tool == "delete_repo" && c.InputJQ != "" {
			hasConfirm = true
		}
	}
	assert.True(t, hasConfirm, "consent-gate must pin the confirm argument")
}

func TestFilterTasks(t *testing.T) {
	tasks := []Task{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	assert.Len(t, filterTasks(tasks, ""), 3)
	got := filterTasks(tasks, "c, a")
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Name)
	assert.Equal(t, "c", got[1].Name)
}
