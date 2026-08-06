package mcp

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

// TestNewDepsLoggerWritesToStderrOnly pins the reason the server gets a logger
// at all: diagnostics must reach the client's log pane (stderr) while stdout
// stays reserved for JSON-RPC frames. A logger pointed at stdout — or no
// logger, as before — would break one half of that contract silently.
func TestNewDepsLoggerWritesToStderrOnly(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	deps := newDeps(&cmdutil.Factory{IOStreams: ios, Version: "test"})

	require.NotNil(t, deps.Logger, "server diagnostics must not be discarded")
	deps.Logger.Warn("server session ended with error", "error", "boom")

	assert.Contains(t, errOut.String(), "server session ended with error",
		"diagnostics belong on stderr")
	assert.Empty(t, out.String(),
		"stdout carries protocol frames only; no log line may land there")
}

// TestNewDepsLoggerLevel pins the verbosity story: a normal session stays
// quiet (warn and above) so agent clients are not flooded, and MELANGE_DEBUG —
// the same switch that turns on API request logging — opens it up to debug.
func TestNewDepsLoggerLevel(t *testing.T) {
	tests := []struct {
		name     string
		debugEnv string
		wantInfo bool
	}{
		{"default is quiet", "", false},
		{"MELANGE_DEBUG=1 is verbose", "1", true},
		{"MELANGE_DEBUG=off stays quiet", "off", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MELANGE_DEBUG", tt.debugEnv)

			ios, _, _, errOut := iostreams.Test()
			logger := stderrLogger(&cmdutil.Factory{IOStreams: ios, Version: "test"})

			ctx := context.Background()
			assert.Equal(t, tt.wantInfo, logger.Enabled(ctx, slog.LevelInfo),
				"info-level chatter should follow MELANGE_DEBUG")
			assert.Equal(t, tt.wantInfo, logger.Enabled(ctx, slog.LevelDebug),
				"debug-level chatter should follow MELANGE_DEBUG")
			assert.True(t, logger.Enabled(ctx, slog.LevelWarn),
				"warnings must always surface")

			logger.Info("server run start")
			if tt.wantInfo {
				assert.Contains(t, errOut.String(), "server run start")
			} else {
				assert.Empty(t, errOut.String())
			}
		})
	}
}
