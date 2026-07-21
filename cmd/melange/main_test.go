package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

// TestRunHelp is the exit-code integration test: --help must exit 0 through
// the real Run() path.
func TestRunHelp(t *testing.T) {
	code := Run([]string{"--help"})
	assert.Equal(t, 0, code, "--help should exit 0")
}

func TestRunUnknownCommand(t *testing.T) {
	code := Run([]string{"definitely-does-not-exist"})
	assert.Equal(t, 2, code, "unknown command should exit 2")
}

func TestRunVersion(t *testing.T) {
	code := Run([]string{"version"})
	assert.Equal(t, 0, code, "melange version should exit 0")
}

func TestRunNoColor(t *testing.T) {
	code := Run([]string{"--no-color", "version"})
	assert.Equal(t, 0, code, "--no-color should be accepted")
}

// TestRunArgCountErrors pins the exit-code contract for positional-arg
// mistakes: they are usage errors and must exit 2, exactly like flag errors.
// One case per command family (repo, model, auth, api, version).
func TestRunArgCountErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"repo view extra arg", []string{"repo", "view", "a", "b"}},
		{"model status missing arg", []string{"model", "status"}},
		{"auth token extra arg", []string{"auth", "token", "extra"}},
		{"api missing path", []string{"api"}},
		{"version extra arg", []string{"version", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := Run(tt.args)
			assert.Equal(t, 2, code, "arg-count errors must exit 2 (usage error)")
		})
	}
}

func TestRunCompletionBash(t *testing.T) {
	code := Run([]string{"completion", "bash"})
	assert.Equal(t, 0, code, "completion bash should exit 0")
}

// TestRunHelpTopicsExitZero pins the help-topic contract through the real
// Run() path: `melange help <topic>` is a successful documentation read and
// must exit 0 for every registered topic (regression: C9 review).
func TestRunHelpTopicsExitZero(t *testing.T) {
	for _, topic := range []string{"environment", "exit-codes", "formatting"} {
		t.Run(topic, func(t *testing.T) {
			code := Run([]string{"help", topic})
			assert.Equal(t, 0, code, "melange help %s must exit 0", topic)
		})
	}
}

// TestHelpContainsExitCodes captures the --help output and asserts it
// documents the exit-code contract (including 130 for interrupted).
func TestHelpContainsExitCodes(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		Executable: "melange",
		Version:    "test",
	}

	cmd := root.NewCmdRoot(f)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs([]string{"--help"})

	require.NoError(t, cmd.Execute(), "--help should not error")

	help := out.String()
	assert.Contains(t, strings.ToLower(help), "exit codes",
		"help output should document the exit-code contract")
	assert.Contains(t, help, "130",
		"help output should mention exit code 130 for interrupted")
}

// TestHelpExamplesAreTruthful pins the root help examples to invocations that
// actually work today: no phantom commands, no missing required flags.
func TestHelpExamplesAreTruthful(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		Executable: "melange",
		Version:    "test",
	}

	cmd := root.NewCmdRoot(f)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs([]string{"--help"})

	require.NoError(t, cmd.Execute(), "--help should not error")

	help := out.String()
	assert.Contains(t, help, "melange repo list --json",
		"help should show a working repo example")
	assert.Contains(t, help, "melange api /v1/me --jq .account.name",
		"help should show a working api example")
	assert.NotContains(t, help, "melange usage",
		"help must not advertise the nonexistent usage command")
	// model upload requires -R ACCOUNT/REPO; the example must include it.
	for _, line := range strings.Split(help, "\n") {
		if strings.Contains(line, "model upload") {
			assert.Contains(t, line, "-R ",
				"model upload example must include the required -R flag")
		}
	}
}
