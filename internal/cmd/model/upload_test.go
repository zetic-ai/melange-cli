package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/upload"
)

type env struct {
	f      *cmdutil.Factory
	reg    *httpmock.Registry
	out    *bytes.Buffer
	errOut *bytes.Buffer
	ctx    context.Context
}

func setup(t *testing.T) *env {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("MELANGE_DEBUG", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("NO_COLOR", "")

	ios, _, out, errOut := iostreams.Test()
	reg := &httpmock.Registry{}
	f := &cmdutil.Factory{
		IOStreams:     ios,
		Version:       "test",
		HTTPTransport: reg,
	}
	f.ApiClient = func() (*api.Client, error) {
		return cmdutil.NewAPIClient(f, "https://api.zetic.ai", "ztp_test")
	}
	return &env{f: f, reg: reg, out: out, errOut: errOut, ctx: context.Background()}
}

func run(t *testing.T, e *env, args ...string) error {
	t.Helper()
	cmd := NewCmdModel(e.f)
	cmd.SetIn(&bytes.Buffer{})
	cmd.SetOut(e.out)
	cmd.SetErr(e.errOut)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(e.ctx)
}

// modelDir creates a model file plus an input file with fixed contents.
func modelDir(t *testing.T) (dir, model, input string) {
	t.Helper()
	dir = t.TempDir()
	model = filepath.Join(dir, "model.onnx")
	input = filepath.Join(dir, "audio.bin")
	require.NoError(t, os.WriteFile(model, bytes.Repeat([]byte("M"), 1000), 0o600))
	require.NoError(t, os.WriteFile(input, bytes.Repeat([]byte("I"), 500), 0o600))
	return dir, model, input
}

const (
	sigF0   = "https://storage.googleapis.com/sig-f0?X-Goog-Signature=SECRETSIG0"
	sigF1   = "https://storage.googleapis.com/sig-f1?X-Goog-Signature=SECRETSIG1"
	sessF0  = "https://storage.googleapis.com/sess-f0?upload_id=SECRETSESSION0"
	sessF1  = "https://storage.googleapis.com/sess-f1?upload_id=SECRETSESSION1"
	repoArg = "zetic/whisper"
)

func sessionBody(files string) string {
	return `{"id":"up_1","state":"UPLOADING","tag":"zt_x","expires_at":"2026-07-21T10:00:00Z","files":[` + files + `]}`
}

func issuedFile(id, canonical, url string) string {
	return fmt.Sprintf(`{"client_file_id":%q,"canonical_path":%q,"upload_url":%q}`, id, canonical, url)
}

// contentRange matches a PUT by its Content-Range header.
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

// assertNoSecretLeaks fails when any output stream contains signed-URL or
// session-URI material.
func assertNoSecretLeaks(t *testing.T, e *env) {
	t.Helper()
	combined := e.out.String() + e.errOut.String()
	for _, secret := range []string{"SECRETSESSION", "SECRETSIG", "X-Goog-Signature", "upload_id=", "storage.googleapis.com"} {
		assert.NotContains(t, combined, secret, "output must never contain upload credentials")
	}
}

// ---------------------------------------------------------------------------
// usage errors
// ---------------------------------------------------------------------------

func TestUploadRequiresRepoFlag(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)
	err := run(t, e, "upload", model)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err), "-R is required: no default_repo fallback for uploads")
	assert.Contains(t, err.Error(), "--repo")
}

func TestUploadRejectsBareRepoName(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)
	err := run(t, e, "upload", "-R", "whisper", model)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "ACCOUNT/REPO")
}

func TestUploadRejectsBucketFlag(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)
	err := run(t, e, "upload", "-R", repoArg, model, "--bucket", "0:1x3x224x224")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "not yet supported")
}

func TestUploadRequiresModelFile(t *testing.T) {
	e := setup(t)
	err := run(t, e, "upload", "-R", repoArg)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
}

func TestUploadManifestFlagExclusive(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)
	err := run(t, e, "upload", "-R", repoArg, model, "--input-manifest", "m.json")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
}

func TestUploadModeFlagsExclusive(t *testing.T) {
	e := setup(t)
	err := run(t, e, "upload", "-R", repoArg, "--resume", "up_1", "--cancel", "up_2")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
}

func TestUploadDryRunExclusiveWithSessionModes(t *testing.T) {
	e := setup(t)
	err := run(t, e, "upload", "-R", repoArg, "--resume", "up_1", "--dry-run")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err), "--dry-run must never reach a mutating mode")
	assert.Empty(t, e.reg.Requests)
}

func TestUploadDuplicateBasenamesExit2(t *testing.T) {
	e := setup(t)
	dir, model, _ := modelDir(t)
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o700))
	dup := filepath.Join(sub, "model.onnx")
	require.NoError(t, os.WriteFile(dup, []byte("x"), 0o600))

	err := run(t, e, "upload", "-R", repoArg, model, "--input", dup)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "model.onnx")
}

// ---------------------------------------------------------------------------
// dry run
// ---------------------------------------------------------------------------

func TestUploadDryRunMakesNoRequests(t *testing.T) {
	e := setup(t)
	// If the command touches the network or auth at all, fail loudly.
	e.f.ApiClient = func() (*api.Client, error) {
		t.Error("--dry-run must not build an API client")
		return nil, fmt.Errorf("no client in dry-run")
	}
	_, model, input := modelDir(t)

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--input", input, "--dry-run"))
	assert.Empty(t, e.reg.Requests, "--dry-run is mutation-free: zero network calls")

	// Non-TTY: stable tab-separated rows on stdout.
	lines := strings.Split(strings.TrimRight(e.out.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, fmt.Sprintf("model\t%s\t1000\typ32Ag==\t{tag}/model.onnx", model), lines[0])
	f1 := strings.Split(lines[1], "\t")
	require.Len(t, f1, 5)
	assert.Equal(t, "input", f1[0])
	assert.Equal(t, "{tag}/inputs/00_audio.bin", f1[4])

	assert.Contains(t, e.errOut.String(), "Dry run", "stderr carries the dry-run header")
	assert.Contains(t, e.errOut.String(), "1.5 KiB", "stderr reports the byte total")
}

func TestUploadDryRunTTYTable(t *testing.T) {
	e := setup(t)
	t.Setenv("NO_COLOR", "1")
	e.f.IOStreams.SetStdoutTTY(true)
	e.f.IOStreams.SetTerminalWidth(200)
	_, model, input := modelDir(t)

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--input", input, "--dry-run"))
	out := e.out.String()
	assert.Contains(t, out, "ROLE")
	assert.Contains(t, out, "DESTINATION")
	assert.Contains(t, out, "1000 B")
	assert.Contains(t, out, "{tag}/inputs/00_audio.bin")
}

func TestUploadDryRunJSON(t *testing.T) {
	e := setup(t)
	_, model, input := modelDir(t)

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--input", input, "--dry-run", "--json"))
	var doc struct {
		Repo  string `json:"repo"`
		Files []struct {
			ClientFileID  string `json:"client_file_id"`
			Role          string `json:"role"`
			Path          string `json:"path"`
			Size          int64  `json:"size"`
			CRC32C        string `json:"crc32c"`
			SHA256        string `json:"sha256"`
			InputIndex    *int   `json:"input_index"`
			CanonicalPath string `json:"canonical_path"`
		} `json:"files"`
		TotalSize int64 `json:"total_size"`
	}
	require.NoError(t, json.Unmarshal(e.out.Bytes(), &doc))
	assert.Equal(t, repoArg, doc.Repo)
	require.Len(t, doc.Files, 2)
	assert.Equal(t, "f0", doc.Files[0].ClientFileID)
	assert.Equal(t, "model", doc.Files[0].Role)
	assert.Equal(t, "{tag}/model.onnx", doc.Files[0].CanonicalPath)
	require.NotNil(t, doc.Files[1].InputIndex)
	assert.Equal(t, 0, *doc.Files[1].InputIndex)
	assert.Equal(t, int64(1500), doc.TotalSize)
	assert.Empty(t, e.reg.Requests)
}

// ---------------------------------------------------------------------------
// happy path
// ---------------------------------------------------------------------------

func TestUploadHappyPath(t *testing.T) {
	e := setup(t)
	t.Setenv("MELANGE_DEBUG", "1") // debug transport must never see GCS traffic
	_, model, input := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(
			issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
				issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--input", input))
	e.reg.Verify(t)

	// Idempotency-Key on create and complete.
	create := e.reg.Requests[0]
	assert.NotEmpty(t, create.Header.Get("Idempotency-Key"))
	complete := e.reg.Requests[len(e.reg.Requests)-1]
	assert.Equal(t, "/v1/repos/zetic/whisper/models/uploads/up_1/complete", complete.URL.Path)
	assert.NotEmpty(t, complete.Header.Get("Idempotency-Key"))

	// The manifest body is a v2 manifest.
	body := requestBody(t, create)
	assert.Equal(t, float64(2), body["manifest_version"])

	// GCS calls carry no PAT; API calls do.
	for _, req := range e.reg.Requests {
		if req.URL.Host == "storage.googleapis.com" {
			assert.Empty(t, req.Header.Get("Authorization"), "GCS must never see the token: %s", req.URL.Path)
		} else {
			assert.Equal(t, "Bearer ztp_test", req.Header.Get("Authorization"))
		}
	}
	// Resumable start protocol header.
	assert.Equal(t, "start", e.reg.Requests[1].Header.Get("x-goog-resumable"))

	// Human result on stderr; per-file completion lines (non-TTY).
	errText := e.errOut.String()
	assert.Contains(t, errText, "model.onnx")
	assert.Contains(t, errText, "audio.bin")
	assert.Contains(t, errText, "m_1")

	// State file removed after successful complete.
	_, err := upload.LoadState("up_1")
	require.ErrorIs(t, err, os.ErrNotExist)

	assertNoSecretLeaks(t, e)
}

func requestBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	require.NotNil(t, req.GetBody)
	rc, err := req.GetBody()
	require.NoError(t, err)
	defer rc.Close() //nolint:errcheck
	raw, err := io.ReadAll(rc)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func TestUploadJSONEmitsCompleteResponse(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--json"))
	assert.JSONEq(t, completeOK(), e.out.String(), "--json emits the complete response verbatim")
}

func TestUploadInputManifestDryRun(t *testing.T) {
	e := setup(t)
	_, model, input := modelDir(t)
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	doc := fmt.Sprintf(`{"manifest_version":2,"files":[{"path":%q,"role":"model"},{"path":%q,"role":"input"}]}`, model, input)
	require.NoError(t, os.WriteFile(manifest, []byte(doc), 0o600))

	require.NoError(t, run(t, e, "upload", "-R", repoArg, "--input-manifest", manifest, "--dry-run"))
	assert.Empty(t, e.reg.Requests)
	assert.Contains(t, e.out.String(), "{tag}/inputs/00_audio.bin")
}

func TestUploadCompleteReportsFailureAsExit1(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200,
			`{"id":"up_1","state":"FAILED","terminal":true,"failure_code":"crc32c_mismatch:f0"}`))

	err := run(t, e, "upload", "-R", repoArg, model)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err), "HTTP 200 with state=FAILED is a failed outcome")
	assert.ErrorIs(t, err, cmdutil.ErrSilent)
	assert.Contains(t, e.errOut.String(), "crc32c_mismatch:f0")

	// FAILED is terminal: keeping the state file (with session URIs) would
	// only make a later --resume confusing.
	_, lerr := upload.LoadState("up_1")
	require.ErrorIs(t, lerr, os.ErrNotExist, "state file must be removed for a terminal FAILED session")
}

func TestUploadActiveSessionConflict(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(409,
			`{"type":"error","error":{"type":"invalid_request_error","message":"an upload session is already active"},"request_id":"req_1"}`))
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads"),
		jsonStub(200,
			`{"count":1,"results":[{"id":"up_9","state":"UPLOADING","created_at":"2026-07-20T10:00:00Z","expires_at":"2026-07-22T10:00:00Z","file_count":2}]}`))

	err := run(t, e, "upload", "-R", repoArg, model)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	errText := e.errOut.String()
	assert.Contains(t, errText, "up_9")
	assert.Contains(t, errText, "melange model upload --resume up_9 -R zetic/whisper")
	assert.Contains(t, errText, "melange model upload --cancel up_9 -R zetic/whisper")
}

// ---------------------------------------------------------------------------
// SIGINT
// ---------------------------------------------------------------------------

func TestUploadSIGINTPreservesSessionAndPrintsResumeHint(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.ctx = ctx
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	// Ctrl-C arrives while the chunk is in flight.
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), func(req *http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	})

	err := run(t, e, "upload", "-R", repoArg, model)
	require.Error(t, err)
	assert.Equal(t, 130, cmdutil.ExitCode(err), "SIGINT exits 130")
	assert.ErrorIs(t, err, cmdutil.ErrSilent, "the command already printed the hint")

	// Session must be preserved (never auto-canceled) with the state intact.
	st, lerr := upload.LoadState("up_1")
	require.NoError(t, lerr, "state file must survive SIGINT")
	assert.Equal(t, sessF0, st.Files[0].SessionURI, "session URI persisted for resume")

	assert.Contains(t, e.errOut.String(), "melange model upload --resume up_1 -R zetic/whisper")
	assertNoSecretLeaks(t, e)
}

// ---------------------------------------------------------------------------
// resume
// ---------------------------------------------------------------------------

func TestResumeWithStateContinuesFromCommittedOffset(t *testing.T) {
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

	// Resume first confirms the session is still resumable server-side.
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

	require.NoError(t, run(t, e, "upload", "--resume", "up_1", "-R", repoArg))
	e.reg.Verify(t) // both GCS stubs consumed: query then tail chunk, nothing else
	assertNoSecretLeaks(t, e)
}

func TestResumeRepoMismatchRejected(t *testing.T) {
	e := setup(t)
	st := &upload.State{SessionID: "up_1", Repo: "zetic/whisper", Tag: "zt_x"}
	require.NoError(t, st.Save())
	err := run(t, e, "upload", "--resume", "up_1", "-R", "acme/other")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
}

func TestResumeWithoutStateFallsBackToServerArrival(t *testing.T) {
	e := setup(t)
	_, model, input := modelDir(t)

	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_7"),
		jsonStub(200, `{
			"id":"up_7","state":"UPLOADING","tag":"zt_y","expires_at":"2026-07-22T10:00:00Z",
			"files":[
				{"client_file_id":"f0","canonical_path":"zt_y/model.onnx","uploaded":true,"verified":false},
				{"client_file_id":"f1","canonical_path":"zt_y/inputs/00_audio.bin","uploaded":false,"verified":false}
			]}`))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_7/files"),
		jsonStub(200,
			`{"expires_at":"2026-07-22T10:00:00Z","files":[`+issuedFile("f1", "zt_y/inputs/00_audio.bin", sigF1)+`]}`))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_7/complete"),
		jsonStub(200, completeOK()))

	require.NoError(t, run(t, e, "upload", "--resume", "up_7", "-R", repoArg, model, "--input", input))
	e.reg.Verify(t)

	// Reissue asked for the missing file only; f0 was never re-uploaded
	// (no GCS stub for it existed, so Verify above proves it).
	var reissue *http.Request
	for _, req := range e.reg.Requests {
		if strings.HasSuffix(req.URL.Path, "/files") {
			reissue = req
		}
	}
	require.NotNil(t, reissue)
	body := requestBody(t, reissue)
	assert.Equal(t, []any{"f1"}, body["client_file_ids"])
}

func TestResumeTerminalServerStateErrorsAndRemovesState(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)
	st := &upload.State{
		SessionID: "up_1",
		Repo:      repoArg,
		Tag:       "zt_x",
		Files: []*upload.StateFile{{
			ClientFileID: "f0", LocalPath: model, CanonicalPath: "zt_x/model.onnx",
			SessionURI: sessF0, Size: 1000,
		}},
	}
	require.NoError(t, st.Save())

	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_1"),
		jsonStub(200, `{"id":"up_1","state":"CANCELED","tag":"zt_x","expires_at":"2026-07-22T10:00:00Z","files":[]}`))

	err := run(t, e, "upload", "--resume", "up_1", "-R", repoArg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session up_1 is canceled; start a new upload")
	e.reg.Verify(t) // no GCS traffic, no reissue: only the session GET

	_, lerr := upload.LoadState("up_1")
	require.ErrorIs(t, lerr, os.ErrNotExist, "terminal sessions must not keep local state")
	assertNoSecretLeaks(t, e)
}

func TestResumeExpiredSessionWithoutStateSaysTerminal(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_7"),
		jsonStub(200, `{"id":"up_7","state":"EXPIRED","tag":"zt_y","expires_at":"2026-07-20T10:00:00Z","files":[]}`))

	err := run(t, e, "upload", "--resume", "up_7", "-R", repoArg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session up_7 is expired; start a new upload")
	assert.NotContains(t, err.Error(), "MODEL_FILE", "terminal state wins over the missing-files hint")
	require.Len(t, e.reg.Requests, 1, "no reissue may be attempted for a terminal session")
}

func TestResumeCorruptStateRebuildsFromServer(t *testing.T) {
	e := setup(t)
	_, model, input := modelDir(t)

	dir, err := upload.StateDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "up_7.json"), []byte("{corrupt"), 0o600))

	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_7"),
		jsonStub(200, `{
			"id":"up_7","state":"UPLOADING","tag":"zt_y","expires_at":"2026-07-22T10:00:00Z",
			"files":[
				{"client_file_id":"f0","canonical_path":"zt_y/model.onnx","uploaded":true,"verified":false},
				{"client_file_id":"f1","canonical_path":"zt_y/inputs/00_audio.bin","uploaded":false,"verified":false}
			]}`))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_7/files"),
		jsonStub(200,
			`{"expires_at":"2026-07-22T10:00:00Z","files":[`+issuedFile("f1", "zt_y/inputs/00_audio.bin", sigF1)+`]}`))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_7/complete"),
		jsonStub(200, completeOK()))

	require.NoError(t, run(t, e, "upload", "--resume", "up_7", "-R", repoArg, model, "--input", input),
		"a corrupt state file must degrade to the rebuild-from-server path")
	e.reg.Verify(t)
	assert.Contains(t, e.errOut.String(), "state file corrupt", "the corruption is surfaced as a warning")
	assertNoSecretLeaks(t, e)
}

func TestResumeWithoutStateAndWithoutFilesErrors(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_7"),
		jsonStub(200, `{
			"id":"up_7","state":"UPLOADING","tag":"zt_y","expires_at":"2026-07-22T10:00:00Z",
			"files":[{"client_file_id":"f0","canonical_path":"zt_y/model.onnx","uploaded":false,"verified":false}]}`))

	err := run(t, e, "upload", "--resume", "up_7", "-R", repoArg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MODEL_FILE")
}

// ---------------------------------------------------------------------------
// reissue on expired signature
// ---------------------------------------------------------------------------

func TestUploadExpiredSignatureReissuesAndRestartsFile(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	// First start attempt: signature expired.
	e.reg.Register(gcsStart("/sig-f0"), jsonStub(403, "expired"))
	// Reissue gives a fresh URL.
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/files"),
		jsonStub(200,
			`{"expires_at":"2026-07-22T10:00:00Z","files":[`+issuedFile("f0", "zt_x/model.onnx", sigF1)+`]}`))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model))
	e.reg.Verify(t)
	assertNoSecretLeaks(t, e)

	// The reissue call is replay-safe server-side (ADR-5): it must carry an
	// Idempotency-Key so the transport can retry it on 5xx.
	for _, req := range e.reg.Requests {
		if req.URL.Path == "/v1/repos/zetic/whisper/models/uploads/up_1/files" {
			assert.NotEmpty(t, req.Header.Get("Idempotency-Key"),
				"reissue must carry an Idempotency-Key")
		}
	}
}

// ---------------------------------------------------------------------------
// --wait
// ---------------------------------------------------------------------------

// fakePoll wires deterministic poll hooks and restores them on cleanup.
func fakePoll(t *testing.T) *fakePollClock {
	t.Helper()
	c := &fakePollClock{now: time.Unix(1000, 0)}
	pollJitter = func(d time.Duration) time.Duration { return d }
	pollSleep = c.sleep
	pollNow = func() time.Time { return c.now }
	t.Cleanup(func() { pollJitter, pollSleep, pollNow = nil, nil, nil })
	return c
}

type fakePollClock struct{ now time.Time }

func (c *fakePollClock) sleep(ctx context.Context, d time.Duration) error {
	c.now = c.now.Add(d)
	return ctx.Err()
}

func statusBody(state string, terminal bool, failure string) string {
	fc := "null"
	if failure != "" {
		fc = fmt.Sprintf("%q", failure)
	}
	return fmt.Sprintf(`{"state":%q,"terminal":%v,"download_ready":%v,"failure_code":%s,
		"created_at":"2026-07-20T10:00:00Z","updated_at":"2026-07-20T10:05:00Z"}`,
		state, terminal, state == "ready", fc)
}

func TestUploadWaitPollsUntilReady(t *testing.T) {
	e := setup(t)
	fakePoll(t)
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))
	statusPath := "/v1/repos/zetic/whisper/models/m_1/status"
	e.reg.Register(httpmock.REST("GET", statusPath), jsonStub(200, statusBody("converting", false, "")))
	e.reg.Register(httpmock.REST("GET", statusPath), jsonStub(200, statusBody("ready", true, "")))

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--wait"))
	e.reg.Verify(t)
	assert.Contains(t, e.out.String(), "ready")
}

func TestUploadWaitTimeout(t *testing.T) {
	e := setup(t)
	fakePoll(t)
	_, model, _ := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))
	statusPath := "/v1/repos/zetic/whisper/models/m_1/status"
	// Never terminal; timeout 5s, initial delay 2s -> polls at 0s, 2s, 5s.
	for range 3 {
		e.reg.Register(httpmock.REST("GET", statusPath), jsonStub(200, statusBody("converting", false, "")))
	}

	err := run(t, e, "upload", "-R", repoArg, model, "--wait", "--timeout", "5s")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, e.errOut.String(), "melange model status m_1 -R zetic/whisper")
}

// ---------------------------------------------------------------------------
// sessions / cancel
// ---------------------------------------------------------------------------

func TestSessionsTable(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads"),
		jsonStub(200, `{"count":1,"results":[
			{"id":"up_9","state":"UPLOADING","created_at":"2026-07-20T10:00:00Z","expires_at":"2026-07-22T10:00:00Z","file_count":2}]}`))

	require.NoError(t, run(t, e, "upload", "--sessions", "-R", repoArg))
	out := e.out.String()
	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "up_9")
	assert.Contains(t, out, "UPLOADING")
	assert.Contains(t, out, "2")
}

func TestSessionsJSON(t *testing.T) {
	e := setup(t)
	body := `{"count":0,"results":[]}`
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads"),
		jsonStub(200, body))
	require.NoError(t, run(t, e, "upload", "--sessions", "-R", repoArg, "--json"))
	assert.JSONEq(t, body, e.out.String())
}

func TestCancelRemovesStateFile(t *testing.T) {
	e := setup(t)
	st := &upload.State{SessionID: "up_1", Repo: repoArg, Tag: "zt_x"}
	require.NoError(t, st.Save())
	e.reg.Register(httpmock.REST("DELETE", "/v1/repos/zetic/whisper/models/uploads/up_1"),
		jsonStub(200, `{"id":"up_1","state":"CANCELED"}`))

	require.NoError(t, run(t, e, "upload", "--cancel", "up_1", "-R", repoArg))
	assert.Contains(t, e.errOut.String(), "✓")
	_, err := upload.LoadState("up_1")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCancelRetriesOn502WithIdempotencyKey(t *testing.T) {
	e := setup(t)
	path := "/v1/repos/zetic/whisper/models/uploads/up_1"
	// Cancel is replay-safe server-side (ADR-5); the Idempotency-Key makes
	// the transport retry the DELETE on a transient 502.
	e.reg.Register(httpmock.REST("DELETE", path), jsonStub(502, `{}`))
	e.reg.Register(httpmock.REST("DELETE", path), jsonStub(200, `{"id":"up_1","state":"CANCELED"}`))

	require.NoError(t, run(t, e, "upload", "--cancel", "up_1", "-R", repoArg))
	e.reg.Verify(t)

	require.Len(t, e.reg.Requests, 2, "the 502 must be retried")
	key := e.reg.Requests[0].Header.Get("Idempotency-Key")
	assert.NotEmpty(t, key, "cancel must carry an Idempotency-Key")
	assert.Equal(t, key, e.reg.Requests[1].Header.Get("Idempotency-Key"),
		"the retry must replay the same key")
	assert.Contains(t, e.errOut.String(), "✓")
}
