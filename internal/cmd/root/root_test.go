package root_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
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
		"MELANGE_DEBUG",
		"1, true, yes, or on (case-insensitive)",
		"NO_COLOR",
		"TERM=dumb",
		"MELANGE_API_KEY > MELANGE_API_KEY_FILE > OS keyring > config file",
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
		"byte-for-byte",
		"--jq",
		"sorted order",
		"--template",
		"tablerow",
		"timeago",
		"json (marshal a value)",
		"tab-separated values with no header",
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
