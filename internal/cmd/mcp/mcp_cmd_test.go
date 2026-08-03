package mcp_test

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

func run(t *testing.T, args ...string) (stdout, stderr *bytes.Buffer, err error) {
	t.Helper()
	ios, in, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Version: "test"}
	cmd := root.NewCmdRoot(f)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	return out, errOut, cmd.ExecuteContext(context.Background())
}

func TestMCPBadTransportIsFlagError(t *testing.T) {
	for _, transport := range []string{"http", "tcp", ""} {
		t.Run("transport="+transport, func(t *testing.T) {
			_, _, err := run(t, "mcp", "--transport", transport)
			require.Error(t, err)
			assert.Equal(t, 2, cmdutil.ExitCode(err), "bad --transport must exit 2")
			assert.Contains(t, err.Error(), "--transport")
			assert.Contains(t, err.Error(), "stdio")
		})
	}
}

func TestMCPRejectsPositionalArgs(t *testing.T) {
	_, _, err := run(t, "mcp", "serve")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
}

func TestMCPHelp(t *testing.T) {
	stdout, _, err := run(t, "mcp", "--help")
	require.NoError(t, err)
	help := stdout.String()
	assert.Contains(t, help, "--transport")
	assert.Contains(t, help, "stdio")
	assert.Contains(t, help, "http")
}
