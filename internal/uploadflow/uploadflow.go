// Package uploadflow is the upload session state machine, extracted from the
// `melange model upload` command so that non-CLI frontends (the MCP server's
// upload_model tool) can drive the same flow.
//
// The package is deliberately presentation-free: it never prints, never
// imports cobra, and reports outcomes through the Events interface, typed
// errors, and the Result. Frontends (the cobra adapter in
// internal/cmd/model, the MCP tool) own all final message formatting, state
// file cleanup on terminal outcomes, and closing the session Lease.
//
// The durable resume-state and GCS primitives live in internal/upload and
// are reused as-is.
package uploadflow

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/upload"
)

// Upload-session states that retain the repository's single active slot
// (ADR-5 vocabulary, compared case-insensitively).
const (
	SessionStateCreated         = "CREATED"
	SessionStateUploading       = "UPLOADING"
	SessionStateVerifying       = "VERIFYING"
	SessionStateDispatchPending = "DISPATCH_PENDING"
)

// Events observes flow progress. Implementations render (CLI stderr) or log
// (MCP); the flow never writes to any stream itself.
type Events interface {
	// Progress reports one file's transfer progress. committed grows with
	// server-acknowledged bytes; committed == total is emitted exactly once
	// per file and signals that file's completion.
	Progress(file string, committed, total int64)
	// Note reports one fully formatted, human-readable flow message
	// (without a trailing newline).
	Note(msg string)
}

// NopEvents discards all events.
type NopEvents struct{}

func (NopEvents) Progress(string, int64, int64) {}
func (NopEvents) Note(string)                   {}

// Orchestrator drives upload sessions: create → transfer (resume/reissue) →
// complete. Zero-value seams select production behavior.
type Orchestrator struct {
	// Gen is the generated API client over the authenticated transport.
	Gen *gen.ClientWithResponses
	// Events receives progress and notes; nil discards them.
	Events Events
	// Bare is the HTTP client used against signed GCS URLs. It must carry
	// NO API transport chain (no Authorization header, no debug logging):
	// signed URLs and resumable session URIs are credentials.
	Bare *http.Client
	// StallTimeout is the per-chunk inactivity budget during transfers.
	StallTimeout time.Duration

	// Poll seams for completion polling: nil selects the real
	// jitter/sleeper/clock in internal/wait.
	Jitter func(time.Duration) time.Duration
	Sleep  func(context.Context, time.Duration) error
	Now    func() time.Time
	// TransferSleep, when non-nil, replaces the GCS uploader's retry
	// backoff sleep (tests inject it; the CLI leaves it nil).
	TransferSleep func(context.Context, time.Duration) error
}

func (o *Orchestrator) events() Events {
	if o.Events != nil {
		return o.Events
	}
	return NopEvents{}
}

func (o *Orchestrator) clockNow() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// ManifestInputs names the local files of one upload, mirroring the CLI's
// upload flag vocabulary. ModelFile is ignored when InputManifest is set;
// exactly one of the two must be provided.
type ManifestInputs struct {
	ModelFile     string
	Inputs        []string
	External      []string
	InputManifest string
	// Buckets are the already-parsed .pt2 shape buckets (the CLI's --bucket
	// flags); only valid together with ModelFile.
	Buckets []upload.BucketSpec
}

// Request describes one fresh upload: the target repository and the
// already-digested local manifest (see BuildSpecs).
type Request struct {
	Account string
	Name    string
	// Repo is ACCOUNT/NAME as displayed and persisted in resume state.
	Repo    string
	Specs   []upload.FileSpec
	Buckets []upload.BucketSpec
	// Wait polls completion (with deliberate replays) until a model
	// reference or terminal state is observable, within Timeout.
	Wait    bool
	Timeout time.Duration
}

// ResumeOptions carries what Resume needs beyond the session id.
type ResumeOptions struct {
	Account string
	Name    string
	Repo    string
	// BuildSpecs lazily digests the local files for a state rebuild; it is
	// invoked only when the local state file is missing or corrupt. nil
	// means the caller provided no local file arguments.
	BuildSpecs func() ([]upload.FileSpec, error)
	Wait       bool
	Timeout    time.Duration
}

// Result is the outcome of a completed flow. Completion carries the raw
// upload-complete response body byte-exact; Model carries the raw model
// object from it (nil when the response has none).
//
// Lease is the held cross-process session lock: the caller owns closing it
// on EVERY non-nil Result. On errors after the lock was acquired, Run and
// Resume return a partial Result (SessionID, Repo, Lease) alongside the
// error so the session identifiers and the lock reach the caller.
type Result struct {
	SessionID string
	Repo      string
	// Response is the typed final completion response (nil on partial
	// results).
	Response *gen.CompleteModelUploadResponse
	// Completion is Response's raw body, byte-exact.
	Completion json.RawMessage
	// Model is the raw "model" object from Completion; nil when absent.
	Model json.RawMessage
	// WaitStarted is the completion clock start, for callers sharing the
	// Wait budget with follow-up polling.
	WaitStarted time.Time
	Lease       *upload.SessionLease
}

// Phase names where in the flow an error occurred, so frontends can attach
// phase-appropriate remediation.
type Phase int

const (
	// PhaseTransfer covers byte transfer to signed URLs (the session is
	// preserved and resumable; acknowledged bytes are never re-sent).
	PhaseTransfer Phase = iota + 1
	// PhaseComplete covers session completion (the session is preserved;
	// completion can be replayed via resume).
	PhaseComplete
)

// SessionError wraps a transfer or completion failure of a preserved,
// resumable session. Err is the underlying cause (context.Canceled for
// interrupts, wait.ErrTimeout for an exhausted Wait budget).
type SessionError struct {
	Phase     Phase
	SessionID string
	Repo      string
	Err       error
}

func (e *SessionError) Error() string { return e.Err.Error() }
func (e *SessionError) Unwrap() error { return e.Err }

// UsageError marks input-shaped problems (the CLI maps them to usage errors,
// exit 2).
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// ConflictError reports a create rejected because an upload session already
// holds the repository's single active slot. SessionID/State identify the
// holder when resolution succeeded (SessionID may be empty). Stale means the
// conflicting session turned terminal during resolution and a retry already
// happened; Err is the original API conflict error.
type ConflictError struct {
	SessionID string
	State     string
	Stale     bool
	Err       error
}

func (e *ConflictError) Error() string { return e.Err.Error() }
func (e *ConflictError) Unwrap() error { return e.Err }

// TerminalStateError reports a resume against a session whose server-side
// state is terminal: it can never be resumed. The caller should discard any
// local resume state for the session.
type TerminalStateError struct {
	SessionID string
	State     string
}

func (e *TerminalStateError) Error() string {
	return fmt.Sprintf("session %s is %s; start a new upload", e.SessionID, lowerState(e.State))
}

// newIdempotencyKey returns a random UUIDv4. Sent as Idempotency-Key on
// create/complete (replay-safe per ADR-5) so the api retry transport may
// safely replay them.
func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; fall back to a
		// timestamp key rather than aborting an upload over it.
		return fmt.Sprintf("melange-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// newIdempotencyKeyParam returns a fresh key as the pointer type the
// generated params structs take. The key is generated once per logical
// call, so the api retry transport replays the same key on 5xx retries.
func newIdempotencyKeyParam() *gen.IdempotencyKey {
	k := gen.IdempotencyKey(newIdempotencyKey())
	return &k
}

// deref returns "" for nil string pointers.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
