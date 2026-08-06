package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/upload"
)

// Signed-URL constants. uploadSignedURLSample matches the fixtures'
// concretized "<signed-url>" placeholder, so the transfer stubs serve the
// fixture round-trips too; the SECRET markers guard log/transcript hygiene.
const (
	uploadSignedURLSample = "https://storage.example/sample?sig=1"
	uploadSigF0           = "https://storage.example/signed-f0?X-Goog-Signature=SECRETSIG0"
	uploadSessF0          = "https://storage.example/session-f0?upload_id=SECRETSESSION0"
)

// uploadCompleteBody is the successful completion response; key order is
// non-alphabetical so a typed re-marshal breaks byte-equality assertions.
const uploadCompleteBody = `{"id":"up_1","state":"CONVERTING","terminal":true,` +
	`"model":{"key":"m_1","version":3}}`

const uploadModelRawRef = `{"key":"m_1","version":3}`

// connectLocal wires a server with the local tool set enabled: the registry
// serves both the authenticated API transport and the bare signed-URL client,
// and durable upload state/locks are isolated into the test's temp dir.
func connectLocal(t *testing.T, reg *httpmock.Registry) (*mcp.ClientSession, *syncWriter) {
	t.Helper()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("LOCALAPPDATA", stateHome)

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	srv := New(Deps{
		Clients: registryProvider(t, reg),
		Version: "test",
		Bare:    &http.Client{Transport: reg},
	}, Options{EnableLocalTools: true})
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Wait() })

	wire := &syncWriter{}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx,
		&mcp.LoggingTransport{Transport: clientTransport, Writer: wire}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession, wire
}

// gcsStart matches a resumable-start POST against a signed URL path.
func gcsStart(path string) httpmock.Matcher {
	return func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == path
	}
}

// gcsPut matches one PUT chunk by path and Content-Range.
func gcsPut(path, contentRange string) httpmock.Matcher {
	return func(req *http.Request) bool {
		return req.Method == http.MethodPut && req.URL.Path == path &&
			req.Header.Get("Content-Range") == contentRange
	}
}

// locationResponse answers a resumable-start with the session URI.
func locationResponse(status int, location string) httpmock.Responder {
	return httpmock.WithHeader(httpmock.StatusStringResponse(status, ""), "Location", location)
}

// registerUploadTransferStubs stubs the GCS exchanges for the three fixture
// files (10, 4, and 4 bytes), all issued the same sample signed URL: one
// resumable-start POST per file answered with a distinct session URI, then
// one full-file PUT each.
func registerUploadTransferStubs(reg *httpmock.Registry) {
	for i, size := range []int{10, 4, 4} {
		sessionPath := fmt.Sprintf("/upload-session/%d", i)
		reg.Register(gcsStart("/sample"),
			locationResponse(201, fmt.Sprintf("https://storage.example%s?upload_id=SECRET%d", sessionPath, i)))
		reg.Register(gcsPut(sessionPath, fmt.Sprintf("bytes 0-%d/%d", size-1, size)),
			httpmock.StatusStringResponse(200, ""))
	}
}

// uploadModelFile writes a 1000-byte model file for the behavior tests.
func uploadModelFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.onnx")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("M", 1000)), 0o600))
	return path
}

func TestUploadModelFullFlowOverStdio(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonBody(201, `{"id":"up_1","state":"UPLOADING","tag":"zt_x",`+
			`"expires_at":"2026-07-21T10:00:00Z","files":[{"client_file_id":"f0",`+
			`"canonical_path":"zt_x/model.onnx","upload_url":"`+uploadSigF0+`"}]}`))
	reg.Register(gcsStart("/signed-f0"), locationResponse(201, uploadSessF0))
	reg.Register(gcsPut("/session-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	completePath := "/v1/repos/zetic/whisper/models/uploads/up_1/complete"
	reg.Register(httpmock.REST("POST", completePath), jsonBody(200, uploadCompleteBody))

	cs, wire := connectLocal(t, reg)
	res := callTool(t, cs, "upload_model", map[string]any{
		"repo": "zetic/whisper", "model_file": uploadModelFile(t),
	})
	require.False(t, res.IsError, "full upload must succeed: %s", textOf(t, res))

	// The envelope carries the completion response and the model reference
	// byte-exact, in the documented shape.
	want := `{"session":` + uploadCompleteBody + `,"model":` + uploadModelRawRef + `}`
	assert.Equal(t, want, textOf(t, res), "text mirror carries the exact envelope")
	structured, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	assert.JSONEq(t, want, string(structured))

	// Create and complete each carried a fresh Idempotency-Key.
	createKey := reg.Requests[0].Header.Get("Idempotency-Key")
	completeKey := reg.Requests[len(reg.Requests)-1].Header.Get("Idempotency-Key")
	assert.NotEmpty(t, createKey)
	assert.NotEmpty(t, completeKey)
	assert.NotEqual(t, createKey, completeKey, "create and complete are distinct logical calls")

	// The GCS exchanges ran over the bare client: no Authorization header may
	// ever reach a signed URL.
	for _, req := range reg.Requests {
		if strings.HasPrefix(req.URL.Host, "storage.example") {
			assert.Empty(t, req.Header.Get("Authorization"),
				"signed storage URLs must never see the API credential")
		}
	}

	// A model reference is the durable handoff: the local session state is
	// cleaned up and the session lock is released.
	_, lerr := upload.LoadState("up_1")
	assert.ErrorIs(t, lerr, os.ErrNotExist, "terminal success removes the resume state")
	lease, err := upload.AcquireSession(context.Background(), "up_1")
	require.NoError(t, err, "the session lock must have been released")
	require.NoError(t, lease.Close())

	// Byte-exactness across the wire: the envelope's raw halves survive.
	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+want)
	reg.Verify(t)
}

func TestUploadModelPendingCompletionKeepsSessionResumable(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonBody(201, `{"id":"up_1","state":"UPLOADING","tag":"zt_x",`+
			`"expires_at":"2026-07-21T10:00:00Z","files":[{"client_file_id":"f0",`+
			`"canonical_path":"zt_x/model.onnx","upload_url":"`+uploadSigF0+`"}]}`))
	reg.Register(gcsStart("/signed-f0"), locationResponse(201, uploadSessF0))
	reg.Register(gcsPut("/session-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	pending := `{"id":"up_1","state":"VERIFYING","terminal":false,"model":null}`
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonBody(200, pending))

	cs, _ := connectLocal(t, reg)
	res := callTool(t, cs, "upload_model", map[string]any{
		"repo": "zetic/whisper", "model_file": uploadModelFile(t),
	})
	require.False(t, res.IsError, "a pending completion is a success, not an error: %s", textOf(t, res))

	// No model yet: the envelope is the session alone, and the session (plus
	// its resume state) stays for a later resume_session_id call.
	assert.Equal(t, `{"session":`+pending+`}`, textOf(t, res))
	_, lerr := upload.LoadState("up_1")
	assert.NoError(t, lerr, "resume state must survive a pending completion")
	lease, err := upload.AcquireSession(context.Background(), "up_1")
	require.NoError(t, err, "the session lock must have been released")
	require.NoError(t, lease.Close())
	reg.Verify(t)
}

func TestUploadModelResumeReplaysCompletionWithoutLocalFiles(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/up_7"),
		jsonBody(200, `{"id":"up_7","state":"VERIFYING","tag":"zt_y",`+
			`"expires_at":"2026-07-22T10:00:00Z","files":[]}`))
	body := `{"id":"up_7","state":"CONVERTING","terminal":true,"model":{"key":"m_7","version":1}}`
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_7/complete"),
		jsonBody(200, body))

	cs, _ := connectLocal(t, reg)
	res := callTool(t, cs, "upload_model", map[string]any{
		"repo": "zetic/whisper", "resume_session_id": "up_7",
	})
	require.False(t, res.IsError,
		"server-owned bytes need no local files on resume: %s", textOf(t, res))

	assert.Equal(t, `{"session":`+body+`,"model":{"key":"m_7","version":1}}`, textOf(t, res))
	require.Len(t, reg.Requests, 2, "detail fetch plus one completion replay")
	reg.Verify(t)
}

func TestUploadModelInterruptedTransferErrorCarriesResumeGuidance(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonBody(201, `{"id":"up_1","state":"UPLOADING","tag":"zt_x",`+
			`"expires_at":"2026-07-21T10:00:00Z","files":[{"client_file_id":"f0",`+
			`"canonical_path":"zt_x/model.onnx","upload_url":"`+uploadSigF0+`"}]}`))
	// The signed URL is dead (non-retryable 4xx) and the reissue fails too:
	// the transfer aborts with the session preserved.
	reg.Register(gcsStart("/signed-f0"), httpmock.StatusStringResponse(400, ""))
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/files"),
		jsonBody(500, `{"type":"error","error":{"type":"api_error","message":"boom"},"request_id":"req_9"}`))

	cs, _ := connectLocal(t, reg)
	res := callTool(t, cs, "upload_model", map[string]any{
		"repo": "zetic/whisper", "model_file": uploadModelFile(t),
	})
	require.True(t, res.IsError)

	text := textOf(t, res)
	assert.Contains(t, text, "up_1", "the error names the preserved session")
	assert.Contains(t, text, `resume_session_id "up_1"`, "the error teaches the resume call")
	assert.Contains(t, text, "never re-sent")
	assert.NotContains(t, text, "SECRETSIG0", "signed URLs must never reach the transcript")

	// The session stays resumable: state kept, lock released.
	_, lerr := upload.LoadState("up_1")
	assert.NoError(t, lerr, "a failed transfer must preserve the resume state")
	lease, err := upload.AcquireSession(context.Background(), "up_1")
	require.NoError(t, err, "the session lock must have been released on the error path")
	require.NoError(t, lease.Close())
	reg.Verify(t)
}

func TestUploadModelWaitPollsCompletionAndConversionOnOneBudget(t *testing.T) {
	clock := usePollHooks(t)
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonBody(201, `{"id":"up_1","state":"UPLOADING","tag":"zt_x",`+
			`"expires_at":"2026-07-21T10:00:00Z","files":[{"client_file_id":"f0",`+
			`"canonical_path":"zt_x/model.onnx","upload_url":"`+uploadSigF0+`"}]}`))
	reg.Register(gcsStart("/signed-f0"), locationResponse(201, uploadSessF0))
	reg.Register(gcsPut("/session-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	completePath := "/v1/repos/zetic/whisper/models/uploads/up_1/complete"
	// Completion needs one replay before the model reference appears...
	reg.Register(httpmock.REST("POST", completePath),
		jsonBody(200, `{"id":"up_1","state":"VERIFYING","terminal":false,"model":null}`))
	reg.Register(httpmock.REST("POST", completePath), jsonBody(200, uploadCompleteBody))
	// ...and conversion needs one backoff step before turning terminal.
	statusPath := "/v1/repos/zetic/whisper/models/m_1/status"
	reg.Register(httpmock.REST("GET", statusPath), jsonBody(200, statusBody("converting", false)))
	finalStatus := statusBody("ready", true)
	reg.Register(httpmock.REST("GET", statusPath), jsonBody(200, finalStatus))

	cs, _ := connectLocal(t, reg)
	res := callTool(t, cs, "upload_model", map[string]any{
		"repo": "zetic/whisper", "model_file": uploadModelFile(t), "wait_seconds": 60,
	})
	require.False(t, res.IsError, "%s", textOf(t, res))

	assert.Equal(t,
		`{"session":`+uploadCompleteBody+`,"model":`+uploadModelRawRef+`,"status":`+finalStatus+`}`,
		textOf(t, res),
		"the wait adds the latest conversion status to the envelope")
	// Exactly one injected backoff sleep, never a real one: the completion
	// replay resolved on its immediate poll and the status poll backed off
	// once — both stages share the deterministic clock.
	assert.Equal(t, []string{"2s"}, sleepStrings(clock.sleeps()),
		"the status poll backed off once on the injected clock")
	reg.Verify(t)
}

// sleepStrings renders recorded sleeps for a compact assertion.
func sleepStrings(sleeps []time.Duration) []string {
	out := make([]string, len(sleeps))
	for i, d := range sleeps {
		out[i] = d.String()
	}
	return out
}

func TestUploadModelWaitStatusFailureKeepsTheUploadResult(t *testing.T) {
	usePollHooks(t)
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonBody(201, `{"id":"up_1","state":"UPLOADING","tag":"zt_x",`+
			`"expires_at":"2026-07-21T10:00:00Z","files":[{"client_file_id":"f0",`+
			`"canonical_path":"zt_x/model.onnx","upload_url":"`+uploadSigF0+`"}]}`))
	reg.Register(gcsStart("/signed-f0"), locationResponse(201, uploadSessF0))
	reg.Register(gcsPut("/session-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonBody(200, uploadCompleteBody))
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/m_1/status"),
		jsonBody(500, `{"type":"error","error":{"type":"api_error","message":"status flaked"},"request_id":"req_2"}`))

	cs, _ := connectLocal(t, reg)
	res := callTool(t, cs, "upload_model", map[string]any{
		"repo": "zetic/whisper", "model_file": uploadModelFile(t), "wait_seconds": 60,
	})

	// The upload succeeded; a failed status readout must not turn that into
	// an error or discard the completion payload — status is simply absent.
	require.False(t, res.IsError, "%s", textOf(t, res))
	assert.Equal(t, `{"session":`+uploadCompleteBody+`,"model":`+uploadModelRawRef+`}`,
		textOf(t, res))
	reg.Verify(t)
}

func TestUploadModelFailedVerificationIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonBody(201, `{"id":"up_1","state":"UPLOADING","tag":"zt_x",`+
			`"expires_at":"2026-07-21T10:00:00Z","files":[{"client_file_id":"f0",`+
			`"canonical_path":"zt_x/model.onnx","upload_url":"`+uploadSigF0+`"}]}`))
	reg.Register(gcsStart("/signed-f0"), locationResponse(201, uploadSessF0))
	reg.Register(gcsPut("/session-f0", "bytes 0-999/1000"), httpmock.StatusStringResponse(200, ""))
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/uploads/up_1/complete"),
		jsonBody(200, `{"id":"up_1","state":"FAILED","terminal":true,"failure_code":"crc32c_mismatch:f0"}`))

	cs, _ := connectLocal(t, reg)
	res := callTool(t, cs, "upload_model", map[string]any{
		"repo": "zetic/whisper", "model_file": uploadModelFile(t),
	})
	require.True(t, res.IsError, "a FAILED verification is an expected failure the caller can act on")

	text := textOf(t, res)
	assert.Contains(t, text, "crc32c_mismatch:f0", "the failure code reaches the caller")
	assert.Contains(t, text, "up_1")
	assert.Contains(t, text, "not resumable")

	// FAILED is terminal: keeping resume state would only mislead a resume.
	_, lerr := upload.LoadState("up_1")
	assert.ErrorIs(t, lerr, os.ErrNotExist)
	reg.Verify(t)
}

func TestUploadModelActiveSessionConflictTeachesResume(t *testing.T) {
	activeID := "0123456789abcdef0123456789abcdef"
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models"),
		jsonBody(409, fmt.Sprintf(
			`{"type":"error","error":{"type":"conflict_error","message":"an upload session is already active","active_upload_id":%q},"request_id":"req_1"}`,
			activeID)))
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/uploads/"+activeID),
		jsonBody(200, fmt.Sprintf(
			`{"id":%q,"state":"UPLOADING","tag":"zt_x","expires_at":"2026-07-22T10:00:00Z","files":[]}`,
			activeID)))

	cs, _ := connectLocal(t, reg)
	res := callTool(t, cs, "upload_model", map[string]any{
		"repo": "zetic/whisper", "model_file": uploadModelFile(t),
	})
	require.True(t, res.IsError)

	text := textOf(t, res)
	assert.Contains(t, text, activeID, "the conflict names the session holding the slot")
	assert.Contains(t, text, "UPLOADING")
	assert.Contains(t, text, "resume_session_id", "the remediation teaches the resume call")
	reg.Verify(t)
}

func TestUploadModelArgumentGatesRejectBeforeAnyRequest(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connectLocal(t, reg)

	// The bounds must reach the advertised schema — that is what makes this
	// test fail if the withWaitBounds refinement is dropped.
	schema, err := json.Marshal(toolNamed(t, cs, "upload_model").InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"maximum":120`, "the wait cap is advertised")
	assert.Contains(t, string(schema), `"minimum":0`, "a negative wait is refused")

	t.Run("wait_seconds beyond the cap", func(t *testing.T) {
		res := callTool(t, cs, "upload_model", map[string]any{
			"repo": "zetic/whisper", "model_file": "model.onnx", "wait_seconds": 121,
		})
		require.True(t, res.IsError)
		assert.Contains(t, textOf(t, res), `validating "arguments"`,
			"the argument is rejected by the schema, not by a failed request")
	})

	t.Run("missing repo", func(t *testing.T) {
		res := callTool(t, cs, "upload_model", map[string]any{"model_file": "model.onnx"})
		require.True(t, res.IsError)
		assert.Contains(t, textOf(t, res), `validating "arguments"`)
	})

	t.Run("missing model_file without a resume", func(t *testing.T) {
		res := callTool(t, cs, "upload_model", map[string]any{"repo": "zetic/whisper"})
		require.True(t, res.IsError)
		assert.Contains(t, textOf(t, res), "model_file is required")
		assert.Contains(t, textOf(t, res), "resume_session_id")
	})

	t.Run("resume id unusable as a state name", func(t *testing.T) {
		res := callTool(t, cs, "upload_model", map[string]any{
			"repo": "zetic/whisper", "resume_session_id": "../etc/passwd",
		})
		require.True(t, res.IsError)
		assert.Contains(t, textOf(t, res), "invalid resume_session_id")
	})

	assert.Empty(t, reg.Requests, "every gate fires before any request is made")
}

func TestUploadModelHiddenWithoutLocalTools(t *testing.T) {
	// Options{} is the HTTP transport's configuration: the catalog must not
	// carry upload_model, and calling it anyway must fail at the protocol
	// layer, not reach a handler.
	cs := connectWith(t, "test", Options{})
	for _, tool := range listAllTools(t, cs) {
		assert.NotEqual(t, "upload_model", tool.Name,
			"upload_model must not be advertised without EnableLocalTools")
	}
	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "upload_model", Arguments: map[string]any{"repo": "zetic/whisper"},
	})
	require.Error(t, err, "an unregistered tool is a protocol error")
}
