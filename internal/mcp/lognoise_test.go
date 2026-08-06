package mcp

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// TestQuietSDKLoggerDropsExactlyTheKnownNoise pins the filter's contract
// record by record: the three SDK per-connection messages are dropped below
// warn, the SAME messages pass at warn and above (an escalated record is no
// longer routine noise), and every other message passes at every level — the
// filter must never become a blanket level raise that eats real diagnostics.
func TestQuietSDKLoggerDropsExactlyTheKnownNoise(t *testing.T) {
	var buf bytes.Buffer
	logger := quietSDKLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	for msg := range sdkNoiseMessages {
		logger.Info(msg, "session_id", "")
		logger.Debug(msg)
	}
	assert.Empty(t, buf.String(), "the known-noisy records must be dropped below warn")

	logger.Warn("server session connected", "session_id", "s1")
	assert.Contains(t, buf.String(), "server session connected",
		"warn and above always pass, even for a noisy message")

	buf.Reset()
	logger.Info("mcp http server listening", "addr", "127.0.0.1:1")
	logger.Debug("schema resolution detail")
	out := buf.String()
	assert.Contains(t, out, "mcp http server listening", "other info records must pass")
	assert.Contains(t, out, "schema resolution detail", "other debug records must pass")

	// The filter survives the handler-derivation paths slog uses for
	// logger.With / groups — a derived handler must keep filtering.
	buf.Reset()
	logger.With("request", "r1").WithGroup("g").Info("server connecting")
	assert.Empty(t, buf.String(), "derived handlers (WithAttrs/WithGroup) must keep filtering")
}

// TestServerSessionsDoNotLogSDKNoise drives a real server through connect and
// disconnect with a debug-floor logger — the loudest configuration either
// transport can produce (MELANGE_DEBUG on stdio, the HTTP daemon's info floor
// is quieter) — and asserts the SDK's three per-connection lines never reach
// the sink while the logger stays usable for everything else. On the
// stateless HTTP transport those lines recur for EVERY request, which is what
// makes them noise rather than diagnostics.
func TestServerSessionsDoNotLogSDKNoise(t *testing.T) {
	var buf bytes.Buffer
	deps := Deps{
		Clients: registryProvider(t, &httpmock.Registry{}),
		Version: "test",
		Logger:  slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	srv := New(deps, Options{})
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	require.NoError(t, clientSession.Close())
	_ = serverSession.Wait()

	out := buf.String()
	for msg := range sdkNoiseMessages {
		assert.NotContains(t, out, msg,
			"the SDK's per-connection %q line must not reach the log sink", msg)
	}
}
