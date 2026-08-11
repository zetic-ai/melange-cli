package root_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/edition"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

// runRoot executes the root command with args and returns stdout and stderr.
func runRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Version: "test"}
	cmd := root.NewCmdRoot(f)
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd.SetIn(&bytes.Buffer{})
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func runQualcommRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	ios, in, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Version: "test", Edition: edition.Qualcomm()}
	cmd := root.NewCmdRoot(f)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestQualcommRootUsesDedicatedBrandingAndNearFullCommandTree(t *testing.T) {
	out, _, err := runQualcommRoot(t, "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "melange-qualcomm")
	assert.Contains(t, out, "Qualcomm-focused")
	for _, command := range []string{"api", "auth", "deploy", "library", "mcp", "model", "plan", "repo", "report", "usage", "version"} {
		assert.Contains(t, out, command)
	}
	assert.NotContains(t, out, "  melange repo")
}

func TestQualcommVersionUsesDedicatedExecutableName(t *testing.T) {
	out, _, err := runQualcommRoot(t, "version")
	require.NoError(t, err)
	assert.Contains(t, out, "melange-qualcomm version")
	assert.NotContains(t, out, "melange version")
}

func TestQualcommNestedHelpAndErrorsUseDedicatedExecutableName(t *testing.T) {
	out, _, err := runQualcommRoot(t, "model", "download", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "melange-qualcomm model targets")
	assert.NotContains(t, out, "`melange model targets`")

	_, _, err = runQualcommRoot(t, "api", "https://example.com/v1/me")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "melange-qualcomm api")
	assert.Contains(t, err.Error(), "melange-qualcomm auth status")
	assert.NotContains(t, err.Error(), "melange api")
}

// ---------------------------------------------------------------------------
// help topics
// ---------------------------------------------------------------------------

func TestHelpEnvironmentTopic(t *testing.T) {
	out, _, err := runRoot(t, "help", "environment")
	require.NoError(t, err)

	for _, want := range []string{
		"MELANGE_API_KEY",
		"MELANGE_API_KEY_FILE",
		"MELANGE_HOST",
		"MELANGE_API_TIMEOUT",
		"MELANGE_DEBUG",
		"1, true, yes, or on (case-insensitive)",
		"NO_COLOR",
		"TERM=dumb",
		"MELANGE_API_KEY > MELANGE_API_KEY_FILE > explicitly selected config",
		"storage > OS keyring > legacy config fallback",
		"melange auth status",
		`${XDG_CONFIG_HOME:-~/.config}/melange/config.yml`,
		`${XDG_STATE_HOME:-~/.local/state}/melange/uploads`,
		`%LocalAppData%\melange\uploads`,
	} {
		assert.Contains(t, out, want)
	}
}

func TestHelpExitCodesTopic(t *testing.T) {
	out, _, err := runRoot(t, "help", "exit-codes")
	require.NoError(t, err)

	for _, want := range []string{
		"0    Success",
		"1    Error",
		"2    Usage error",
		"4    Authentication error",
		"130  Interrupted",
		"Agent guidance",
	} {
		assert.Contains(t, out, want)
	}
}

func TestHelpFormattingTopic(t *testing.T) {
	out, _, err := runRoot(t, "help", "formatting")
	require.NoError(t, err)

	for _, want := range []string{
		"--json",
		"exactly one trailing newline",
		`{"model": ..., "status": ...}`,
		"model download --json redacts signed artifact URLs",
		"--jq",
		"sorted order",
		"--template",
		"tablerow",
		"timeago",
		"json (marshal a value)",
		"tab-separated values with no header",
		"reversible backslash escapes",
		`\\, \t, \r, and \n`,
		`{"results": [...], "count": N}`,
		"--paginate",
		"carried through from the\nlast page",
	} {
		assert.Contains(t, out, want)
	}
}

func TestHelpTopicViaFlag(t *testing.T) {
	out, _, err := runRoot(t, "environment", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "MELANGE_API_KEY")
}

func TestHelpTopicsHiddenFromRootHelp(t *testing.T) {
	out, _, err := runRoot(t, "--help")
	require.NoError(t, err)
	// A topic's Short only ever renders in a command listing, so its absence
	// proves the topics are hidden from the Available Commands section.
	assert.NotContains(t, out, "Exit codes returned by melange",
		"help topics must not be listed as commands")
	assert.Contains(t, out, "melange help environment", "root help must point at the reference topics")
}

func TestCommandGroupsRejectUnknownSubcommands(t *testing.T) {
	for _, group := range []string{"auth", "repo", "model", "report", "library", "deploy"} {
		t.Run(group, func(t *testing.T) {
			out, _, err := runRoot(t, group, "typoo")
			require.Error(t, err)
			assert.Equal(t, 2, cmdutil.ExitCode(err))
			assert.Contains(t, err.Error(), "unknown command \"typoo\"")
			assert.Empty(t, out, "usage errors must not write help to the data stream")
		})
	}
}

func TestCommandGroupsWithoutSubcommandStillShowHelp(t *testing.T) {
	for _, group := range []string{"auth", "repo", "model", "report", "library", "deploy"} {
		t.Run(group, func(t *testing.T) {
			out, _, err := runRoot(t, group)
			require.NoError(t, err)
			assert.Contains(t, out, "Usage:")
		})
	}
}

func TestUserFacingHelpDoesNotPresentCompactFakeIDsAsRunnableExamples(t *testing.T) {
	commands := [][]string{
		{"model", "--help"},
		{"model", "view", "--help"},
		{"model", "targets", "--help"},
		{"model", "set-default", "--help"},
		{"model", "status", "--help"},
		{"model", "download", "--help"},
		{"model", "upload", "--help"},
		{"report", "--help"},
		{"report", "view", "--help"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args[:len(args)-1], "_"), func(t *testing.T) {
			out, _, err := runRoot(t, args...)
			require.NoError(t, err)
			for _, fictional := range []string{"m_ab12cd", "tm_71", "up_ab12cd"} {
				assert.NotContains(t, out, fictional)
			}
		})
	}
}
