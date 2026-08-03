package uploadflow_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/upload"
	"github.com/zetic-ai/melange-cli/internal/uploadflow"
	"github.com/zetic-ai/melange-cli/internal/wait"
)

const (
	sigF0   = "https://storage.googleapis.com/sig-f0?X-Goog-Signature=SECRETSIG0"
	sigF1   = "https://storage.googleapis.com/sig-f1?X-Goog-Signature=SECRETSIG1"
	sessF0  = "https://storage.googleapis.com/sess-f0?upload_id=SECRETSESSION0"
	sessF1  = "https://storage.googleapis.com/sess-f1?upload_id=SECRETSESSION1"
	repoArg = "zetic/whisper"
)

// progressEvent records one Events.Progress call.
type progressEvent struct {
	File             string
	Committed, Total int64
}

// eventRecorder captures the flow's observable events for assertions.
type eventRecorder struct {
	notes    []string
	progress []progressEvent
}

func (r *eventRecorder) Progress(file string, committed, total int64) {
	r.progress = append(r.progress, progressEvent{File: file, Committed: committed, Total: total})
}

func (r *eventRecorder) Note(msg string) { r.notes = append(r.notes, msg) }

// fakeClock is an injected poll clock: sleeps advance it instantly, so no
// test ever waits on real time.
type fakeClock struct{ now time.Time }

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	c.now = c.now.Add(d)
	return ctx.Err()
}

type env struct {
	reg    *httpmock.Registry
	orch   *uploadflow.Orchestrator
	events *eventRecorder
	clock  *fakeClock
	ctx    context.Context
}

func setup(t *testing.T) *env {
	t.Helper()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("LOCALAPPDATA", stateHome)

	reg := &httpmock.Registry{}
	g, err := gen.NewClientWithResponses("https://api.zetic.ai",
		gen.WithHTTPClient(&http.Client{Transport: reg}))
	require.NoError(t, err)

	events := &eventRecorder{}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	orch := &uploadflow.Orchestrator{
		Gen:    g,
		Events: events,
		Bare:   &http.Client{Transport: reg},
		Jitter: func(d time.Duration) time.Duration { return d },
		Sleep:  clock.sleep,
		Now:    func() time.Time { return clock.now },
		TransferSleep: func(ctx context.Context, d time.Duration) error {
			t.Errorf("unexpected transfer retry sleep of %s: tests must not wait on real backoff", d)
			return ctx.Err()
		},
	}
	return &env{reg: reg, orch: orch, events: events, clock: clock, ctx: context.Background()}
}

// closeResult releases the lease the flow hands back on every non-nil Result.
func closeResult(t *testing.T, res *uploadflow.Result) {
	t.Helper()
	if res != nil && res.Lease != nil {
		require.NoError(t, res.Lease.Close())
	}
}

// modelDir creates a model file plus an input file with fixed contents.
func modelDir(t *testing.T) (dir, model, input string) {
	t.Helper()
	dir = t.TempDir()
	model = filepath.Join(dir, "model.onnx")
	input = filepath.Join(dir, "audio.bin")
	require.NoError(t, os.WriteFile(model, []byte(repeat("M", 1000)), 0o600))
	require.NoError(t, os.WriteFile(input, []byte(repeat("I", 500)), 0o600))
	return dir, model, input
}

func repeat(s string, n int) string {
	out := make([]byte, 0, n*len(s))
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

func specsFor(t *testing.T, model string, inputs ...string) []upload.FileSpec {
	t.Helper()
	specs, buckets, err := uploadflow.BuildSpecs(uploadflow.ManifestInputs{
		ModelFile: model, Inputs: inputs,
	}, nil)
	require.NoError(t, err)
	require.Empty(t, buckets)
	return specs
}

func request(specs []upload.FileSpec) uploadflow.Request {
	return uploadflow.Request{Account: "zetic", Name: "whisper", Repo: repoArg, Specs: specs}
}

func sessionBody(files string) string {
	return `{"id":"up_1","state":"UPLOADING","tag":"zt_x","expires_at":"2026-07-21T10:00:00Z","files":[` + files + `]}`
}

func issuedFile(id, canonical, url string) string {
	return fmt.Sprintf(`{"client_file_id":%q,"canonical_path":%q,"upload_url":%q}`, id, canonical, url)
}

func gcsPut(path, contentRange string) httpmock.Matcher {
	return func(req *http.Request) bool {
		return req.Method == http.MethodPut && req.URL.Path == path &&
			req.Header.Get("Content-Range") == contentRange
	}
}

func gcsStart(path string) httpmock.Matcher {
	return func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == path
	}
}

func locationResponse(status int, location string) httpmock.Responder {
	return httpmock.WithHeader(httpmock.StatusStringResponse(status, ""), "Location", location)
}

// jsonStub responds with an application/json content type, which the
// generated client requires to populate its typed JSONxx fields.
func jsonStub(status int, body string) httpmock.Responder {
	return httpmock.WithHeader(httpmock.StatusStringResponse(status, body), "Content-Type", "application/json")
}

func completeOK() string {
	return `{"id":"up_1","state":"CONVERTING","terminal":true,"model":{"key":"m_1","version":3}}`
}

// ---------------------------------------------------------------------------
// create: Idempotency-Key 201/200-replay semantics
// ---------------------------------------------------------------------------

func TestRunAcceptsIdempotentCreateReplay(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)

	// HTTP 200 instead of 201: the server replayed an identical earlier
	// create (same Idempotency-Key semantics). The flow must proceed as if
	// the session were fresh.
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(200, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	res, err := e.orch.Run(e.ctx, request(specsFor(t, model)))
	require.NoError(t, err)
	defer closeResult(t, res)
	e.reg.Verify(t)

	assert.NotEmpty(t, e.reg.Requests[0].Header.Get("Idempotency-Key"),
		"create must carry an Idempotency-Key so a replay is safe")
	assert.Equal(t, "up_1", res.SessionID)
	assert.Equal(t, repoArg, res.Repo)
	assert.JSONEq(t, completeOK(), string(res.Completion), "Completion carries the raw response body")
	assert.JSONEq(t, `{"key":"m_1","version":3}`, string(res.Model), "Model carries the raw model object")
	require.NotNil(t, res.Response.Model)
	assert.Equal(t, "m_1", res.Response.Model.Key)

	// State cleanup on terminal outcomes belongs to the frontend, never the
	// flow: the state file must still exist here.
	_, lerr := upload.LoadState("up_1")
	require.NoError(t, lerr)
}

func TestRunCreateAndCompleteUseFreshIdempotencyKeys(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	res, err := e.orch.Run(e.ctx, request(specsFor(t, model)))
	require.NoError(t, err)
	defer closeResult(t, res)

	create := e.reg.Requests[0].Header.Get("Idempotency-Key")
	complete := e.reg.Requests[len(e.reg.Requests)-1].Header.Get("Idempotency-Key")
	assert.NotEmpty(t, create)
	assert.NotEmpty(t, complete)
	assert.NotEqual(t, create, complete, "create and complete are distinct logical calls")
}

func TestRunConflictYieldsTypedConflictError(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)
	activeID := "0123456789abcdef0123456789abcdef"

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(409, fmt.Sprintf(
			`{"type":"error","error":{"type":"conflict_error","message":"an upload session is already active","active_upload_id":%q},"request_id":"req_1"}`,
			activeID)))
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/"+activeID),
		jsonStub(200, fmt.Sprintf(
			`{"id":%q,"state":"UPLOADING","tag":"zt_x","expires_at":"2026-07-22T10:00:00Z","files":[]}`,
			activeID)))

	res, err := e.orch.Run(e.ctx, request(specsFor(t, model)))
	require.Error(t, err)
	assert.Nil(t, res, "no session exists yet, so no partial result and no lease")

	var cerr *uploadflow.ConflictError
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, activeID, cerr.SessionID)
	assert.Equal(t, "UPLOADING", cerr.State)
	assert.False(t, cerr.Stale)
	assert.Contains(t, cerr.Err.Error(), "already active")
	e.reg.Verify(t)
}

func TestRunStaleConflictRetriesOnceWithNote(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)
	activeID := "0123456789abcdef0123456789abcdef"

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(409, fmt.Sprintf(
			`{"type":"error","error":{"type":"conflict_error","message":"an upload session is already active","active_upload_id":%q},"request_id":"req_1"}`,
			activeID)))
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/"+activeID),
		jsonStub(200, fmt.Sprintf(
			`{"id":%q,"state":"CONVERTING","tag":"zt_old","expires_at":"2026-07-22T10:00:00Z","files":[]}`,
			activeID)))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	res, err := e.orch.Run(e.ctx, request(specsFor(t, model)))
	require.NoError(t, err)
	defer closeResult(t, res)
	e.reg.Verify(t)
	assert.Contains(t, e.events.notes,
		"The conflicting session finished while the upload was starting; retrying once.")
}

// ---------------------------------------------------------------------------
// transfer: resume offset query and chunk retry
// ---------------------------------------------------------------------------

func TestResumeContinuesFromServerCommittedOffset(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)

	st := &upload.State{
		SessionID: "up_1",
		Repo:      repoArg,
		Tag:       "zt_x",
		Files: []*upload.StateFile{{
			ClientFileID:  "f0",
			LocalPath:     model,
			CanonicalPath: "zt_x/model.onnx",
			UploadURL:     sigF0,
			SessionURI:    sessF0,
			Size:          1000,
			CRC32C:        "yp32Ag==",
			Offset:        999, // stale hint: the server's answer is authoritative
		}},
	}
	require.NoError(t, st.Save())

	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_1"),
		jsonStub(200, `{
			"id":"up_1","state":"UPLOADING","tag":"zt_x","expires_at":"2026-07-22T10:00:00Z",
			"files":[{"client_file_id":"f0","canonical_path":"zt_x/model.onnx","uploaded":false,"verified":false}]}`))
	// Committed-offset query: 500 bytes are already there.
	e.reg.Register(gcsPut("/sess-f0", "bytes */1000"),
		httpmock.WithHeader(httpmock.StatusStringResponse(308, ""), "Range", "bytes=0-499"))
	// The remaining bytes only — starting exactly at 500.
	e.reg.Register(gcsPut("/sess-f0", "bytes 500-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	res, err := e.orch.Resume(e.ctx, "up_1", uploadflow.ResumeOptions{
		Account: "zetic", Name: "whisper", Repo: repoArg,
	})
	require.NoError(t, err)
	defer closeResult(t, res)
	e.reg.Verify(t) // both GCS stubs consumed: query then tail chunk, nothing else

	assert.Contains(t, e.events.notes, "Resuming upload session up_1 (1 files)")
	assert.Equal(t, []progressEvent{{File: "model.onnx", Committed: 1000, Total: 1000}},
		e.events.progress, "committed == total is the single completion event")
}

func TestTransferChunkRetryRequeriesOffsetAndNeverResendsAckedBytes(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	// The first chunk attempt dies with a retryable 503 — but the server
	// actually committed half the file.
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(503, ""))
	e.reg.Register(gcsPut("/sess-f0", "bytes */1000"),
		httpmock.WithHeader(httpmock.StatusStringResponse(308, ""), "Range", "bytes=0-499"))
	// Only the unacknowledged tail is re-sent.
	e.reg.Register(gcsPut("/sess-f0", "bytes 500-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	res, err := e.orch.Run(e.ctx, request(specsFor(t, model)))
	require.NoError(t, err)
	defer closeResult(t, res)
	e.reg.Verify(t)

	// Forward progress restores retry credit without any backoff sleep (the
	// TransferSleep seam fails the test if one happens).
	assert.Equal(t, []progressEvent{
		{File: "model.onnx", Committed: 500, Total: 1000},
		{File: "model.onnx", Committed: 1000, Total: 1000},
	}, e.events.progress)
}

// ---------------------------------------------------------------------------
// completion: recoverable vs terminal
// ---------------------------------------------------------------------------

func TestRunWaitReplaysCompletionUntilModelWithFreshKeys(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)
	start := e.clock.now

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	completePath := "/v1/repos/zetic/whisper/models/uploads/up_1/complete"
	e.reg.Register(httpmock.REST("POST", completePath),
		jsonStub(200, `{"id":"up_1","state":"VERIFYING","terminal":false,"model":null}`))
	e.reg.Register(httpmock.REST("POST", completePath),
		jsonStub(200, `{"id":"up_1","state":"VERIFYING","terminal":false,"model":null}`))
	e.reg.Register(httpmock.REST("POST", completePath), jsonStub(200, completeOK()))

	req := request(specsFor(t, model))
	req.Wait = true
	req.Timeout = 5 * time.Second
	res, err := e.orch.Run(e.ctx, req)
	require.NoError(t, err)
	defer closeResult(t, res)
	e.reg.Verify(t)

	assert.JSONEq(t, `{"key":"m_1","version":3}`, string(res.Model))
	assert.Equal(t, start, res.WaitStarted, "WaitStarted anchors the shared budget at completion start")
	assert.Equal(t, 2*time.Second, e.clock.now.Sub(start),
		"one injected backoff sleep separates the replays; no real time passes")

	keys := map[string]struct{}{}
	for _, r := range e.reg.Requests {
		if r.URL.Path == completePath {
			key := r.Header.Get("Idempotency-Key")
			assert.NotEmpty(t, key)
			keys[key] = struct{}{}
		}
	}
	assert.Len(t, keys, 3, "every deliberate replay must bypass prior intermediate responses")
}

func TestRunWaitCompletionTimeoutPreservesSession(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	completePath := "/v1/repos/zetic/whisper/models/uploads/up_1/complete"
	for range 4 {
		e.reg.Register(httpmock.REST("POST", completePath),
			jsonStub(200, `{"id":"up_1","state":"VERIFYING","terminal":false,"model":null}`))
	}

	req := request(specsFor(t, model))
	req.Wait = true
	req.Timeout = 5 * time.Second
	res, err := e.orch.Run(e.ctx, req)
	require.Error(t, err)
	defer closeResult(t, res)
	e.reg.Verify(t)

	var serr *uploadflow.SessionError
	require.ErrorAs(t, err, &serr)
	assert.Equal(t, uploadflow.PhaseComplete, serr.Phase)
	assert.Equal(t, "up_1", serr.SessionID)
	assert.ErrorIs(t, err, wait.ErrTimeout)

	require.NotNil(t, res, "errors after session creation return a partial result")
	assert.Equal(t, "up_1", res.SessionID)
	require.NotNil(t, res.Lease, "the caller owns the still-held session lock")
	_, lerr := upload.LoadState("up_1")
	require.NoError(t, lerr, "a completion timeout must leave the session resumable")
}

func TestRunCompletionTerminalOutcomes(t *testing.T) {
	t.Run("FAILED carries the failure code and raw body", func(t *testing.T) {
		e := setup(t)
		_, model, _ := modelDir(t)
		body := `{"id":"up_1","state":"FAILED","terminal":true,"failure_code":"crc32c_mismatch:f0"}`

		e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
			jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
		e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
		e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
		e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
			jsonStub(200, body))

		res, err := e.orch.Run(e.ctx, request(specsFor(t, model)))
		require.NoError(t, err, "a FAILED completion is a result, not a flow error: frontends own the verdict")
		defer closeResult(t, res)

		assert.Equal(t, "FAILED", string(res.Response.State))
		require.NotNil(t, res.Response.FailureCode)
		assert.Equal(t, "crc32c_mismatch:f0", *res.Response.FailureCode)
		assert.Nil(t, res.Model)
		assert.JSONEq(t, body, string(res.Completion))
		assert.False(t, uploadflow.TerminalCompletionWithoutModel(res.Response),
			"FAILED is its own verdict, distinct from canceled/expired")
	})

	t.Run("CANCELED without a model is terminal-without-model", func(t *testing.T) {
		e := setup(t)
		_, model, _ := modelDir(t)

		e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
			jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
		e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
		e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
		e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
			jsonStub(200, `{"id":"up_1","state":"CANCELED","terminal":true,"model":null}`))

		res, err := e.orch.Run(e.ctx, request(specsFor(t, model)))
		require.NoError(t, err)
		defer closeResult(t, res)

		assert.Nil(t, res.Model)
		assert.True(t, uploadflow.TerminalCompletionWithoutModel(res.Response))
	})
}

func TestResumeRecoverableStateReplaysCompletionWithoutLocalFiles(t *testing.T) {
	e := setup(t)

	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_7"),
		jsonStub(200, `{"id":"up_7","state":"VERIFYING","tag":"zt_y",`+
			`"expires_at":"2026-07-22T10:00:00Z","files":[]}`))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_7/complete"),
		jsonStub(200, `{"id":"up_7","state":"CONVERTING","terminal":true,"model":{"key":"m_7","version":1}}`))

	res, err := e.orch.Resume(e.ctx, "up_7", uploadflow.ResumeOptions{
		Account: "zetic", Name: "whisper", Repo: repoArg,
	})
	require.NoError(t, err, "server-owned bytes need no local artifacts and no BuildSpecs")
	defer closeResult(t, res)
	e.reg.Verify(t)
	assert.JSONEq(t, `{"key":"m_7","version":1}`, string(res.Model))
}

func TestResumeTerminalStateReturnsTypedError(t *testing.T) {
	e := setup(t)
	st := &upload.State{SessionID: "up_1", Repo: repoArg, Tag: "zt_x"}
	require.NoError(t, st.Save())

	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_1"),
		jsonStub(200, `{"id":"up_1","state":"CANCELED","tag":"zt_x","expires_at":"2026-07-22T10:00:00Z","files":[]}`))

	res, err := e.orch.Resume(e.ctx, "up_1", uploadflow.ResumeOptions{
		Account: "zetic", Name: "whisper", Repo: repoArg,
	})
	require.Error(t, err)
	defer closeResult(t, res)

	var terr *uploadflow.TerminalStateError
	require.ErrorAs(t, err, &terr)
	assert.Equal(t, "up_1", terr.SessionID)
	assert.Equal(t, "CANCELED", terr.State)
	assert.Equal(t, "session up_1 is canceled; start a new upload", terr.Error())

	// State cleanup for terminal sessions is the frontend's decision.
	_, lerr := upload.LoadState("up_1")
	require.NoError(t, lerr)
}

// ---------------------------------------------------------------------------
// context cancellation mid-transfer
// ---------------------------------------------------------------------------

func TestRunContextCancelMidTransferPreservesSession(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	// Ctrl-C arrives while the first chunk is in flight.
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), func(req *http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	})

	input := filepath.Join(filepath.Dir(model), "audio.bin")
	res, err := e.orch.Run(ctx, request(specsFor(t, model, input)))
	require.Error(t, err)
	defer closeResult(t, res)

	var serr *uploadflow.SessionError
	require.ErrorAs(t, err, &serr)
	assert.Equal(t, uploadflow.PhaseTransfer, serr.Phase)
	assert.Equal(t, "up_1", serr.SessionID)
	assert.Equal(t, repoArg, serr.Repo)
	assert.ErrorIs(t, err, context.Canceled)

	require.NotNil(t, res)
	assert.Equal(t, "up_1", res.SessionID)
	require.NotNil(t, res.Lease)

	// Session must be preserved (never auto-canceled) with the state intact.
	st, lerr := upload.LoadState("up_1")
	require.NoError(t, lerr, "state file must survive cancellation")
	assert.Equal(t, sessF0, st.Files[0].SessionURI, "session URI persisted for resume")
	assert.False(t, st.Files[0].Uploaded)
}

// ---------------------------------------------------------------------------
// manifest input validation
// ---------------------------------------------------------------------------

func TestBuildSpecsUsageErrors(t *testing.T) {
	t.Run("missing model file", func(t *testing.T) {
		_, _, err := uploadflow.BuildSpecs(uploadflow.ManifestInputs{}, nil)
		var uerr *uploadflow.UsageError
		require.ErrorAs(t, err, &uerr)
		assert.Contains(t, uerr.Error(), "MODEL_FILE is required")
	})

	t.Run("duplicate basenames", func(t *testing.T) {
		dir, model, _ := modelDir(t)
		sub := filepath.Join(dir, "sub")
		require.NoError(t, os.Mkdir(sub, 0o700))
		dup := filepath.Join(sub, "model.onnx")
		require.NoError(t, os.WriteFile(dup, []byte("x"), 0o600))

		_, _, err := uploadflow.BuildSpecs(uploadflow.ManifestInputs{
			ModelFile: model, Inputs: []string{dup},
		}, nil)
		var uerr *uploadflow.UsageError
		require.ErrorAs(t, err, &uerr)
		assert.ErrorIs(t, err, upload.ErrDuplicateFilename)
	})
}
