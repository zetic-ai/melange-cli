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

func TestRunCompletionBash(t *testing.T) {
	code := Run([]string{"completion", "bash"})
	assert.Equal(t, 0, code, "completion bash should exit 0")
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
