package mcp

import (
	"context"
	"log/slog"
)

// sdkNoiseMessages are the exact per-connection INFO records go-sdk v1.7.0's
// mcp.Server emits (mcp/server.go: Connect, bind, disconnect). On the
// stateless HTTP transport every request builds its own server, so each POST
// costs all three lines — with an empty session_id, since stateless mode
// issues none — and on stdio they narrate the one obvious session. Zero
// information either way, and enough volume to bury real diagnostics in every
// client's log pane.
//
// Filtering is by exact message, never by level alone: any other record the
// SDK or this package logs below warn (schema faults, connect errors are
// Error anyway) still flows, so genuinely useful diagnostics survive.
var sdkNoiseMessages = map[string]bool{
	"server connecting":           true,
	"server session connected":    true,
	"server session disconnected": true,
}

// quietSDKLogger wraps logger so the known-noisy SDK records are dropped
// below warn level. Warn and above always pass — if the SDK ever escalates
// one of these messages, it is by definition no longer routine noise. Both
// transports get this: New installs it on every server it builds.
func quietSDKLogger(logger *slog.Logger) *slog.Logger {
	return slog.New(sdkNoiseFilter{handler: logger.Handler()})
}

// sdkNoiseFilter is a pass-through slog.Handler that drops sdkNoiseMessages
// below warn. It only ever discards records — it never rewrites, stores, or
// re-emits them — so it cannot weaken the credential-hygiene property of the
// logger it wraps (what does not reach the sink through it is exactly what
// reached it, minus the noise).
type sdkNoiseFilter struct {
	handler slog.Handler
}

func (f sdkNoiseFilter) Enabled(ctx context.Context, level slog.Level) bool {
	return f.handler.Enabled(ctx, level)
}

func (f sdkNoiseFilter) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < slog.LevelWarn && sdkNoiseMessages[r.Message] {
		return nil
	}
	return f.handler.Handle(ctx, r)
}

func (f sdkNoiseFilter) WithAttrs(attrs []slog.Attr) slog.Handler {
	return sdkNoiseFilter{handler: f.handler.WithAttrs(attrs)}
}

func (f sdkNoiseFilter) WithGroup(name string) slog.Handler {
	return sdkNoiseFilter{handler: f.handler.WithGroup(name)}
}
