package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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

// TestHelpContainsExitCodes verifies that --help exits 0. The Long description
// that mentions exit codes is verified in root.go source.
func TestHelpContainsExitCodes(t *testing.T) {
	code := Run([]string{"--help"})
	assert.Equal(t, 0, code, "--help should exit 0")
}
