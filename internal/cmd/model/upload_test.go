package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/text"
	"github.com/zetic-ai/melange-cli/internal/upload"
	"github.com/zetic-ai/melange-cli/internal/uploadflow"
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
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("LOCALAPPDATA", stateHome)
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
	sigF2   = "https://storage.googleapis.com/sig-f2?X-Goog-Signature=SECRETSIG2"
	sessF0  = "https://storage.googleapis.com/sess-f0?upload_id=SECRETSESSION0"
	sessF1  = "https://storage.googleapis.com/sess-f1?upload_id=SECRETSESSION1"
	sessF2  = "https://storage.googleapis.com/sess-f2?upload_id=SECRETSESSION2"
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

func registerFreshUploadTransfer(e *env) {
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
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

func TestUploadBucketFlagValidatesCompleteSetsBeforeNetwork(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	model := filepath.Join(dir, "model.pt2")
	input := filepath.Join(dir, "input.npy")
	require.NoError(t, os.WriteFile(model, []byte("model"), 0o600))
	require.NoError(t, os.WriteFile(input, []byte("input"), 0o600))
	err := run(t, e, "upload", "-R", repoArg, model,
		"--bucket", "0:1x3x224x224", "--bucket", "1:1x3x384x384",
		"--input", input, "--dry-run")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "same number of inputs")
	assert.Empty(t, e.reg.Requests)
}

func TestUploadBucketDryRunRendersOptionsAndCanonicalPaths(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	model := filepath.Join(dir, "model.pt2")
	require.NoError(t, os.WriteFile(model, []byte("model"), 0o600))
	var inputs []string
	for _, name := range []string{"b0x.npy", "b0y.npy", "b1x.npy", "b1y.npy"} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(name), 0o600))
		inputs = append(inputs, path)
	}
	args := []string{"upload", "-R", repoArg, model,
		"--bucket", "0:1x3x224x224", "--bucket", "1:1x3x384x384"}
	for _, input := range inputs {
		args = append(args, "--input", input)
	}
	args = append(args, "--dry-run", "--json")
	require.NoError(t, run(t, e, args...))

	var got struct {
		Options struct {
			Buckets []upload.BucketSpec `json:"buckets"`
		} `json:"options"`
		Files []struct {
			BucketIndex   *int   `json:"bucket_index"`
			CanonicalPath string `json:"canonical_path"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal(e.out.Bytes(), &got))
	assert.Equal(t, []upload.BucketSpec{
		{Index: 0, Dims: []int{1, 3, 224, 224}},
		{Index: 1, Dims: []int{1, 3, 384, 384}},
	}, got.Options.Buckets)
	require.NotNil(t, got.Files[4].BucketIndex)
	assert.Equal(t, 1, *got.Files[4].BucketIndex)
	assert.Equal(t, "{tag}/inputs/bucket_1/01_b1y.npy", got.Files[4].CanonicalPath)
	assert.Empty(t, e.reg.Requests)
}

func TestManifestOptionsPreservesBucketWireShape(t *testing.T) {
	assert.Nil(t, uploadflow.ManifestOptions(nil), "ordinary uploads must omit options")

	got := uploadflow.ManifestOptions([]upload.BucketSpec{
		{Index: 0, Dims: []int{1, 3, 224, 224}},
		{Index: 7, Dims: []int{2, 80}},
	})
	require.NotNil(t, got)
	require.NotNil(t, got.Buckets)
	assert.Equal(t, []gen.ManifestBucket{
		{Index: 0, Dims: []int{1, 3, 224, 224}},
		{Index: 7, Dims: []int{2, 80}},
	}, *got.Buckets, "validated bucket indexes and typed dimensions must reach the API unchanged")
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

func TestUploadDryRunAllowsModelOnly(t *testing.T) {
	e := setup(t)
	_, model, _ := modelDir(t)

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--dry-run"))
	assert.Empty(t, e.reg.Requests)
	lines := strings.Split(strings.TrimSpace(e.out.String()), "\n")
	require.Len(t, lines, 1)
	fields := strings.Split(lines[0], "\t")
	require.Len(t, fields, 5)
	assert.Equal(t, "model", fields[0])
	assert.Equal(t, text.EscapeTSVCell(model), fields[1])
	assert.Equal(t, "{tag}/model.onnx", fields[4])
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
	assert.Equal(t, fmt.Sprintf("model\t%s\t1000\typ32Ag==\t{tag}/model.onnx",
		text.EscapeTSVCell(model)), lines[0])
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

func TestUploadPersistsSessionURIBeforeSendingBytes(t *testing.T) {
	e := setup(t)
	_, model, input := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(
			issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
				issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), func(req *http.Request) (*http.Response, error) {
		// Initial state persistence has succeeded. Break the state directory
		// entry exactly before the new bearer session URI must be saved. Do
		// not remove the parent because the session lock remains open on
		// Windows.
		stateDir, err := upload.StateDir()
		require.NoError(t, err)
		statePath := filepath.Join(stateDir, "up_1.json")
		require.NoError(t, os.Remove(statePath))
		require.NoError(t, os.Mkdir(statePath, 0o700))
		return locationResponse(201, sessF0)(req)
	})

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persisting resumable upload session")
	require.Len(t, e.reg.Requests, 2,
		"no upload PUT may occur until the resumable session URI is durable")
	assert.Equal(t, http.MethodPost, e.reg.Requests[1].Method)
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
	_, model, input := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--input", input, "--json"))
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
	_, model, input := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200,
			`{"id":"up_1","state":"FAILED","terminal":true,"failure_code":"crc32c_mismatch:f0"}`))

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err), "HTTP 200 with state=FAILED is a failed outcome")
	assert.ErrorIs(t, err, cmdutil.ErrSilent)
	assert.Contains(t, e.errOut.String(), "crc32c_mismatch:f0")

	// FAILED is terminal: keeping the state file (with session URIs) would
	// only make a later --resume confusing.
	_, lerr := upload.LoadState("up_1")
	require.ErrorIs(t, lerr, os.ErrNotExist, "state file must be removed for a terminal FAILED session")
}

func TestUploadOldServerActiveSessionConflictRecognizesEverySlotState(t *testing.T) {
	for _, state := range []string{"CREATED", "UPLOADING", "VERIFYING", "DISPATCH_PENDING"} {
		t.Run(state, func(t *testing.T) {
			e := setup(t)
			_, model, input := modelDir(t)

			e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
				jsonStub(409,
					`{"type":"error","error":{"type":"invalid_request_error","message":"an upload session is already active"},"request_id":"req_1"}`))
			e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads"),
				jsonStub(200, fmt.Sprintf(
					`{"count":1,"results":[{"id":"up_9","state":%q,"created_at":"2026-07-20T10:00:00Z","expires_at":"2026-07-22T10:00:00Z","file_count":2}]}`,
					state)))

			err := run(t, e, "upload", "-R", repoArg, model, "--input", input)
			require.Error(t, err)
			assert.Equal(t, 1, cmdutil.ExitCode(err))
			assert.ErrorIs(t, err, cmdutil.ErrSilent)
			assertActiveConflictGuidance(t, e.errOut.String(), "up_9", state)
			e.reg.Verify(t)
		})
	}
}

func TestUploadStructuredActiveSessionConflictUsesResolvedState(t *testing.T) {
	for _, state := range []string{"CREATED", "UPLOADING", "VERIFYING", "DISPATCH_PENDING"} {
		t.Run(state, func(t *testing.T) {
			e := setup(t)
			_, model, input := modelDir(t)
			activeID := "0123456789abcdef0123456789abcdef"

			e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
				jsonStub(409,
					fmt.Sprintf(`{"type":"error","error":{"type":"conflict_error","message":"an upload session is already active","active_upload_id":%q},"request_id":"req_1"}`, activeID)))
			e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/"+activeID),
				jsonStub(200, fmt.Sprintf(
					`{"id":%q,"state":%q,"tag":"zt_x","expires_at":"2026-07-22T10:00:00Z","files":[]}`,
					activeID, state)))

			err := run(t, e, "upload", "-R", repoArg, model, "--input", input)
			require.Error(t, err)
			assert.Equal(t, 1, cmdutil.ExitCode(err))
			assert.ErrorIs(t, err, cmdutil.ErrSilent)
			assertActiveConflictGuidance(t, e.errOut.String(), activeID, state)
			e.reg.Verify(t)
			require.Len(t, e.reg.Requests, 2, "a structured conflict must resolve the exact session without listing all sessions")
		})
	}
}

func TestUploadRetriesOnceWhenConflictSessionAlreadyTurnedTerminal(t *testing.T) {
	e := setup(t)
	_, model, input := modelDir(t)
	activeID := "0123456789abcdef0123456789abcdef"

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(409,
			fmt.Sprintf(`{"type":"error","error":{"type":"conflict_error","message":"an upload session is already active","active_upload_id":%q},"request_id":"req_1"}`, activeID)))
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/"+activeID),
		jsonStub(200, fmt.Sprintf(
			`{"id":%q,"state":"CONVERTING","tag":"zt_old","expires_at":"2026-07-22T10:00:00Z","files":[]}`,
			activeID)))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--input", input))
	e.reg.Verify(t)
	assert.Contains(t, e.errOut.String(), "finished while the upload was starting; retrying once")
}

func TestUploadHelpGuardsEmptyResumeSession(t *testing.T) {
	e := setup(t)

	require.NoError(t, run(t, e, "upload", "--help"))
	assert.Contains(t, e.out.String(), "// empty")
	assert.Contains(t, e.out.String(), `[ -n "$session_id" ]`)
}

func assertActiveConflictGuidance(t *testing.T, stderr, sessionID, state string) {
	t.Helper()
	assert.Contains(t, stderr, sessionID)
	assert.Contains(t, stderr, state)
	assert.Contains(t, stderr,
		"melange api /v1/repos/zetic/whisper/models/uploads/"+sessionID+" --jq .state")
	switch state {
	case "CREATED", "UPLOADING":
		assert.Contains(t, stderr, "melange model upload --resume "+sessionID+" -R zetic/whisper")
		assert.Contains(t, stderr, "melange model upload --cancel "+sessionID+" -R zetic/whisper")
	case "VERIFYING":
		assert.Contains(t, stderr,
			"melange model upload --resume "+sessionID+" -R zetic/whisper --wait")
		assert.NotContains(t, stderr, "--cancel")
	case "DISPATCH_PENDING":
		assert.Contains(t, stderr,
			"melange model upload --resume "+sessionID+" -R zetic/whisper --wait")
		assert.NotContains(t, stderr, "--cancel")
	}
}

// ---------------------------------------------------------------------------
// SIGINT
// ---------------------------------------------------------------------------

func TestUploadSIGINTPreservesSessionAndPrintsResumeHint(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.ctx = ctx
	_, model, input := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	// Ctrl-C arrives while the chunk is in flight.
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), func(req *http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	})

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input)
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
	assert.Empty(t, reissue.Header.Get("Idempotency-Key"),
		"resume reissue must also mint fresh URLs without a replay header")
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
	_, model, input := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF2))))
	// First start attempt: signature expired.
	e.reg.Register(gcsStart("/sig-f0"), jsonStub(403, "expired"))
	// Reissue gives a fresh URL.
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/files"),
		jsonStub(200,
			`{"expires_at":"2026-07-22T10:00:00Z","files":[`+issuedFile("f0", "zt_x/model.onnx", sigF1)+`]}`))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(gcsStart("/sig-f2"), locationResponse(201, sessF2))
	e.reg.Register(gcsPut("/sess-f2", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--input", input))
	e.reg.Verify(t)
	assertNoSecretLeaks(t, e)

	// Reissue intentionally mints fresh signed URLs on every call. It must not
	// carry an Idempotency-Key or be replayed by the mutation retry transport.
	for _, req := range e.reg.Requests {
		if req.URL.Path == "/v1/repos/zetic/whisper/models/uploads/up_1/files" {
			assert.Empty(t, req.Header.Get("Idempotency-Key"),
				"reissue must mint fresh URLs rather than replay an old response")
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
	_, model, input := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))
	statusPath := "/v1/repos/zetic/whisper/models/m_1/status"
	e.reg.Register(httpmock.REST("GET", statusPath), jsonStub(200, statusBody("converting", false, "")))
	e.reg.Register(httpmock.REST("GET", statusPath), jsonStub(200, statusBody("ready", true, "")))

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--input", input, "--wait"))
	e.reg.Verify(t)
	assert.Contains(t, e.out.String(), "ready")
}

func TestUploadWaitJSONPreservesModelAndFinalStatus(t *testing.T) {
	e := setup(t)
	fakePoll(t)
	_, model, input := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, `{"id":"up_1","state":"CONVERTING","terminal":true,`+
			`"model":{"key":"m_1","version":3,"future_field":"preserved"}}`))
	statusPath := "/v1/repos/zetic/whisper/models/m_1/status"
	e.reg.Register(httpmock.REST("GET", statusPath), jsonStub(200, statusBody("ready", true, "")))

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--input", input, "--wait", "--json"))
	var got struct {
		Model  gen.ModelRef            `json:"model"`
		Status gen.ModelStatusResponse `json:"status"`
	}
	require.NoError(t, json.Unmarshal(e.out.Bytes(), &got))
	assert.Equal(t, "m_1", got.Model.Key)
	assert.Equal(t, 3, got.Model.Version)
	assert.Equal(t, gen.ModelStatusResponseStateReady, got.Status.State)
	assert.True(t, got.Status.Terminal)
	var raw struct {
		Model map[string]any `json:"model"`
	}
	require.NoError(t, json.Unmarshal(e.out.Bytes(), &raw))
	assert.Equal(t, "preserved", raw.Model["future_field"],
		"the original model object must survive the waited envelope")
	e.reg.Verify(t)
}

func TestUploadWaitTimeout(t *testing.T) {
	e := setup(t)
	fakePoll(t)
	_, model, input := modelDir(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))
	statusPath := "/v1/repos/zetic/whisper/models/m_1/status"
	// Never terminal; timeout 5s, initial delay 2s -> polls at 0s, 2s, 5s.
	for range 3 {
		e.reg.Register(httpmock.REST("GET", statusPath), jsonStub(200, statusBody("converting", false, "")))
	}

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input, "--wait", "--timeout", "5s")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, e.errOut.String(), "melange model status m_1 -R zetic/whisper")
}

func TestUploadWaitTimeoutBoundsInitialCompletionRequest(t *testing.T) {
	e := setup(t)
	_, model, input := modelDir(t)
	registerFreshUploadTransfer(e)

	requestHadDeadline := false
	var requestBudget time.Duration
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		func(req *http.Request) (*http.Response, error) {
			deadline, ok := req.Context().Deadline()
			requestHadDeadline = ok
			requestBudget = time.Until(deadline)
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(500 * time.Millisecond):
				return nil, errors.New("initial completion request outlived --timeout")
			}
		})

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input,
		"--wait", "--timeout", "20ms")

	require.Error(t, err)
	assert.True(t, requestHadDeadline, "the initial completion call must receive the shared wait deadline")
	assert.LessOrEqual(t, requestBudget, 100*time.Millisecond,
		"the request deadline must be the advertised wait budget, not a longer transport timeout")
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, e.errOut.String(), "Timed out after 20ms waiting for upload completion")
	_, loadErr := upload.LoadState("up_1")
	require.NoError(t, loadErr, "a timed-out completion request must preserve resumable state")
}

func TestUploadWaitCancellationDuringInitialCompletionRemainsSIGINT(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.ctx = ctx
	_, model, input := modelDir(t)
	registerFreshUploadTransfer(e)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		func(req *http.Request) (*http.Response, error) {
			cancel()
			<-req.Context().Done()
			return nil, req.Context().Err()
		})

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input,
		"--wait", "--timeout", "1s")

	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, err, cmdutil.ErrSilent)
	assert.Equal(t, 130, cmdutil.ExitCode(err))
	assert.Contains(t, e.errOut.String(), "Interrupted")
	_, loadErr := upload.LoadState("up_1")
	require.NoError(t, loadErr, "Ctrl-C during completion must preserve resumable state")
}

func TestUploadWaitRecoversModelReferenceBeforeCleaningState(t *testing.T) {
	e := setup(t)
	fakePoll(t)
	_, model, input := modelDir(t)
	registerFreshUploadTransfer(e)

	completePath := "/v1/repos/zetic/whisper/models/uploads/up_1/complete"
	assertStateExists := func(req *http.Request) (*http.Response, error) {
		_, err := upload.LoadState("up_1")
		require.NoError(t, err, "state must remain recoverable until completion returns a model")
		return jsonStub(200,
			`{"id":"up_1","state":"VERIFYING","terminal":false,"model":null}`)(req)
	}
	e.reg.Register(httpmock.REST("POST", completePath), assertStateExists)
	e.reg.Register(httpmock.REST("POST", completePath), func(req *http.Request) (*http.Response, error) {
		_, err := upload.LoadState("up_1")
		require.NoError(t, err, "state must still exist during deliberate completion replay")
		return jsonStub(200,
			`{"id":"up_1","state":"CONVERTING","terminal":true,"model":{"key":"m_1","version":3}}`)(req)
	})
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/m_1/status"),
		func(req *http.Request) (*http.Response, error) {
			_, err := upload.LoadState("up_1")
			require.ErrorIs(t, err, os.ErrNotExist,
				"state must be cleaned before conversion polling once the model reference is durable")
			return jsonStub(200, statusBody("ready", true, ""))(req)
		})

	require.NoError(t, run(t, e, "upload", "-R", repoArg, model, "--input", input, "--wait"))
	e.reg.Verify(t)

	var keys []string
	for _, req := range e.reg.Requests {
		if req.URL.Path == completePath {
			keys = append(keys, req.Header.Get("Idempotency-Key"))
		}
	}
	require.Len(t, keys, 2)
	assert.NotEmpty(t, keys[0])
	assert.NotEmpty(t, keys[1])
	assert.NotEqual(t, keys[0], keys[1],
		"a deliberate completion replay must not reuse a cached intermediate response")
}

func TestUploadWaitNullModelTimeoutPreservesRecoverableState(t *testing.T) {
	e := setup(t)
	fakePoll(t)
	_, model, input := modelDir(t)
	registerFreshUploadTransfer(e)

	completePath := "/v1/repos/zetic/whisper/models/uploads/up_1/complete"
	for range 4 {
		e.reg.Register(httpmock.REST("POST", completePath),
			jsonStub(200, `{"id":"up_1","state":"VERIFYING","terminal":false,"model":null}`))
	}

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input,
		"--wait", "--timeout", "5s")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, e.errOut.String(), "Timed out after 5s")
	assert.Contains(t, e.errOut.String(), "melange model upload --resume up_1 -R zetic/whisper")
	_, loadErr := upload.LoadState("up_1")
	require.NoError(t, loadErr, "a null-model timeout must leave the session resumable")
	e.reg.Verify(t)

	keys := map[string]struct{}{}
	for _, req := range e.reg.Requests {
		if req.URL.Path != completePath {
			continue
		}
		key := req.Header.Get("Idempotency-Key")
		assert.NotEmpty(t, key)
		keys[key] = struct{}{}
	}
	assert.Len(t, keys, 4, "every deliberate replay must bypass prior intermediate responses")
}

func TestUploadAmbiguousCompleteTimeoutCanResumeWithFreshKey(t *testing.T) {
	e := setup(t)
	_, model, input := modelDir(t)
	registerFreshUploadTransfer(e)

	completePath := "/v1/repos/zetic/whisper/models/uploads/up_1/complete"
	e.reg.Register(httpmock.REST("POST", completePath),
		func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "session is preserved")
	_, loadErr := upload.LoadState("up_1")
	require.NoError(t, loadErr, "an ambiguous complete timeout must preserve recovery state")

	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_1"),
		jsonStub(200, `{"id":"up_1","state":"VERIFYING","tag":"zt_x",`+
			`"expires_at":"2026-07-22T10:00:00Z","files":[]}`))
	e.reg.Register(httpmock.REST("POST", completePath), jsonStub(200, completeOK()))

	require.NoError(t, run(t, e, "upload", "--resume", "up_1", "-R", repoArg))
	e.reg.Verify(t)
	_, loadErr = upload.LoadState("up_1")
	require.ErrorIs(t, loadErr, os.ErrNotExist)

	var keys []string
	for _, req := range e.reg.Requests {
		if req.URL.Path == completePath {
			keys = append(keys, req.Header.Get("Idempotency-Key"))
		}
	}
	require.Len(t, keys, 2)
	assert.NotEqual(t, keys[0], keys[1],
		"resume must not reuse the key from an ambiguously completed request")
}

func TestResumeServerOwnedCompletionStateNeedsNoLocalArtifacts(t *testing.T) {
	for _, state := range []string{"VERIFYING", "DISPATCH_PENDING", "CONVERTING"} {
		t.Run(state, func(t *testing.T) {
			e := setup(t)
			e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_7"),
				jsonStub(200, fmt.Sprintf(`{"id":"up_7","state":%q,"tag":"zt_y",`+
					`"expires_at":"2026-07-22T10:00:00Z","files":[]}`, state)))
			e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_7/complete"),
				jsonStub(200, `{"id":"up_7","state":"CONVERTING","terminal":true,`+
					`"model":{"key":"m_7","version":1}}`))

			require.NoError(t, run(t, e, "upload", "--resume", "up_7", "-R", repoArg))
			e.reg.Verify(t)
			assert.NotContains(t, e.errOut.String(), "MODEL_FILE")
		})
	}
}

func TestResumeCompletionReplayTerminalFailureCleansState(t *testing.T) {
	e := setup(t)
	st := &upload.State{SessionID: "up_7", Repo: repoArg, Tag: "zt_y"}
	require.NoError(t, st.Save())
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_7"),
		jsonStub(200, `{"id":"up_7","state":"DISPATCH_PENDING","tag":"zt_y",`+
			`"expires_at":"2026-07-22T10:00:00Z","files":[]}`))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_7/complete"),
		jsonStub(200, `{"id":"up_7","state":"FAILED","terminal":true,`+
			`"failure_code":"dispatch_failed"}`))

	err := run(t, e, "upload", "--resume", "up_7", "-R", repoArg)
	require.ErrorIs(t, err, cmdutil.ErrSilent)
	assert.Contains(t, e.errOut.String(), "dispatch_failed")
	_, loadErr := upload.LoadState("up_7")
	require.ErrorIs(t, loadErr, os.ErrNotExist, "terminal failure must clean stale resumable state")
	e.reg.Verify(t)
}

func TestUploadWaitSharesBudgetBetweenCompletionAndConversion(t *testing.T) {
	e := setup(t)
	clock := fakePoll(t)
	start := clock.now
	_, model, input := modelDir(t)
	registerFreshUploadTransfer(e)

	completePath := "/v1/repos/zetic/whisper/models/uploads/up_1/complete"
	e.reg.Register(httpmock.REST("POST", completePath),
		jsonStub(200, `{"id":"up_1","state":"VERIFYING","terminal":false,"model":null}`))
	e.reg.Register(httpmock.REST("POST", completePath),
		jsonStub(200, `{"id":"up_1","state":"VERIFYING","terminal":false,"model":null}`))
	e.reg.Register(httpmock.REST("POST", completePath),
		jsonStub(200, `{"id":"up_1","state":"CONVERTING","terminal":true,`+
			`"model":{"key":"m_1","version":3}}`))
	statusPath := "/v1/repos/zetic/whisper/models/m_1/status"
	for range 3 {
		e.reg.Register(httpmock.REST("GET", statusPath),
			jsonStub(200, statusBody("converting", false, "")))
	}

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input,
		"--wait", "--timeout", "5s")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Equal(t, 5*time.Second, clock.now.Sub(start),
		"completion replay and conversion polling must consume one shared --timeout budget")
	assert.Contains(t, e.errOut.String(), "Timed out after 5s",
		"the user-facing timeout must describe the total shared budget, not only the remainder")
	e.reg.Verify(t)
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

	require.NoError(t, run(t, e, "upload", "--cancel", "up_1", "-R", repoArg, "--yes"))
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

	require.NoError(t, run(t, e, "upload", "--cancel", "up_1", "-R", repoArg, "--yes"))
	e.reg.Verify(t)

	require.Len(t, e.reg.Requests, 2, "the 502 must be retried")
	key := e.reg.Requests[0].Header.Get("Idempotency-Key")
	assert.NotEmpty(t, key, "cancel must carry an Idempotency-Key")
	assert.Equal(t, key, e.reg.Requests[1].Header.Get("Idempotency-Key"),
		"the retry must replay the same key")
	assert.Contains(t, e.errOut.String(), "✓")
}

func TestCancelNonInteractiveRequiresYesWithoutRequest(t *testing.T) {
	e := setup(t)

	err := run(t, e, "upload", "--cancel", "up_1", "-R", repoArg)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--yes")
	assert.Empty(t, e.reg.Requests, "cancellation must not reach the API without confirmation")
}

func TestCancelRejectsUnsafeSessionIDBeforePromptOrRequest(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	e.f.IOStreams.In = strings.NewReader("unused\n")
	sessionID := "up_safe\x1b]52;c;U0VDUkVU\a"

	err := run(t, e, "upload", "--cancel", sessionID, "-R", repoArg)

	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.NotContains(t, e.errOut.String(), "\x1b")
	assert.Empty(t, e.reg.Requests)
}

func TestCancelNoInputRequiresYesWithoutRequest(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	e.f.NoInput = true

	err := run(t, e, "upload", "--cancel", "up_1", "-R", repoArg)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests, "--no-input without --yes must make no request")
}

func TestCancelInteractiveRequiresExactSessionID(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	e.f.IOStreams.In = strings.NewReader("up_1\n")
	e.reg.Register(httpmock.REST("DELETE", "/v1/repos/zetic/whisper/models/uploads/up_1"),
		jsonStub(200, `{"id":"up_1","state":"CANCELED"}`))

	require.NoError(t, run(t, e, "upload", "--cancel", "up_1", "-R", repoArg))
	require.Len(t, e.reg.Requests, 1)
	assert.Contains(t, e.errOut.String(), "Type up_1 to confirm")
}

func TestCancelInteractiveMismatchMakesNoRequest(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	e.f.IOStreams.In = strings.NewReader("up_other\n")

	err := run(t, e, "upload", "--cancel", "up_1", "-R", repoArg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not canceled")
	assert.Empty(t, e.reg.Requests)
}

func TestCancelInteractiveWhitespacePaddingIsNotExact(t *testing.T) {
	for _, typed := range []string{" up_1\n", "up_1 \n", "\tup_1\n", "up_1\t\n"} {
		t.Run(fmt.Sprintf("%q", typed), func(t *testing.T) {
			e := setup(t)
			e.f.IOStreams.SetStdinTTY(true)
			e.f.IOStreams.In = strings.NewReader(typed)

			err := run(t, e, "upload", "--cancel", "up_1", "-R", repoArg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not canceled")
			assert.Empty(t, e.reg.Requests)
		})
	}
}

type blockingReader struct{ release <-chan struct{} }

func (r blockingReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

func TestCancelInteractivePromptHonorsContextCancellation(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	release := make(chan struct{})
	e.f.IOStreams.In = blockingReader{release: release}
	ctx, cancel := context.WithCancel(context.Background())
	cancelTimer := time.AfterFunc(20*time.Millisecond, cancel)
	defer cancelTimer.Stop()
	time.AfterFunc(100*time.Millisecond, func() { close(release) })
	e.ctx = ctx

	err := run(t, e, "upload", "--cancel", "up_1", "-R", repoArg)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 130, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestActiveSessionGuidanceNeutralizesServerControlledOSC52(t *testing.T) {
	e := setup(t)
	opts := &uploadOptions{f: e.f, repo: "safe/repo"}
	printActiveSessionGuidance(opts,
		"up_safe\x1b]52;c;U0VTU0lPTl9TRUNSRVQ=\a_id",
		"UPLOADING\x1b]52;c;U1RBVEVfU0VDUkVU\a")

	assert.Contains(t, e.errOut.String(), "up_safe_id")
	assert.Contains(t, e.errOut.String(), "UPLOADING")
	assert.NotContains(t, e.errOut.String(), "\x1b")
	assert.NotContains(t, e.errOut.String(), "U0VTU0lPTl9TRUNSRVQ")
	assert.NotContains(t, e.errOut.String(), "U1RBVEVfU0VDUkVU")
}

func TestProgressNeutralizesControlSequencesInLocalFilename(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStderrTTY(true)
	p := newProgress(e.f, "model\x1b]52;c;RklMRV9TRUNSRVQ=\a.onnx", 100)

	p.update(50)
	p.done()

	output := e.errOut.String()
	assert.Contains(t, output, "model.onnx")
	assert.NotContains(t, output, "\x1b")
	assert.NotContains(t, output, "RklMRV9TRUNSRVQ")
}

func TestUploadWaitReleasesSessionLockAndSanitizesFailure(t *testing.T) {
	e := setup(t)
	fakePoll(t)
	_, model, input := modelDir(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonStub(201, sessionBody(issuedFile("f0", "zt_x/model.onnx", sigF0)+","+
			issuedFile("f1", "zt_x/inputs/00_audio.bin", sigF1))))
	e.reg.Register(gcsStart("/sig-f0"), locationResponse(201, sessF0))
	e.reg.Register(gcsPut("/sess-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(gcsStart("/sig-f1"), locationResponse(201, sessF1))
	e.reg.Register(gcsPut("/sess-f1", "bytes 0-499/500"), httpmock.StatusStringResponse(200, ""))
	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonStub(200, completeOK()))
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/m_1/status"),
		func(req *http.Request) (*http.Response, error) {
			lockCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			lease, err := upload.AcquireSession(lockCtx, "up_1")
			if err != nil {
				return nil, fmt.Errorf("upload session lock remained held during conversion wait: %w", err)
			}
			require.NoError(t, lease.Close())
			return jsonStub(200, `{"state":"failed","terminal":true,"download_ready":false,`+
				`"failure_code":"safe\u001b]52;c;RkFJTFVSRV9TRUNSRVQ=\u0007 text",`+
				`"created_at":"2026-07-20T10:00:00Z","updated_at":"2026-07-20T10:05:00Z"}`)(req)
		})

	err := run(t, e, "upload", "-R", repoArg, model, "--input", input, "--wait")
	require.ErrorIs(t, err, cmdutil.ErrSilent)
	assert.Contains(t, e.errOut.String(), "safe text")
	assert.NotContains(t, e.errOut.String(), "\x1b")
	assert.NotContains(t, e.errOut.String(), "RkFJTFVSRV9TRUNSRVQ")
}
