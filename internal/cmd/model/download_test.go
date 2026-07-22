package model_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

const (
	authPath    = "/v1/repos/zetic/whisper/models/m_ab12cd/targets/tm_71/download-authorizations"
	signedURL   = "https://storage.googleapis.com/dl/model.bin?X-Goog-Signature=SECRETSIG"
	artifactVal = "converted model bytes"
)

func TestMain(m *testing.M) {
	stateHome, err := os.MkdirTemp("", "melange-download-test-state-*")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_STATE_HOME", stateHome); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(stateHome)
	os.Exit(code)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func crc32cB64(s string) string {
	sum := crc32.Checksum([]byte(s), crc32.MakeTable(crc32.Castagnoli))
	var be [4]byte
	binary.BigEndian.PutUint32(be[:], sum)
	return base64.StdEncoding.EncodeToString(be[:])
}

// authBody builds a DownloadAuthorizationResponse with one artifact.
func authBody(checksum string) string {
	return authBodyForURL(signedURL, checksum)
}

func authBodyForURL(artifactURL, checksum string) string {
	cs := "null"
	if checksum != "" {
		cs = fmt.Sprintf("%q", checksum)
	}
	return fmt.Sprintf(`{"authorization_id":"da_1","expires_at":"2026-07-21T12:00:00Z",`+
		`"artifacts":[{"name":"model.bin","url":%q,"size":%d,"checksum":%s}]}`,
		artifactURL, len(artifactVal), cs)
}

func twoArtifactAuthBody(firstURL, secondURL string) string {
	return fmt.Sprintf(`{"authorization_id":"da_1","expires_at":"2026-07-21T12:00:00Z","artifacts":[`+
		`{"name":"model.bin","url":%q,"size":%d,"checksum":"sha256:%s"},`+
		`{"name":"weights.bin","url":%q,"size":%d,"checksum":"sha256:%s"}]}`,
		firstURL, len(artifactVal), sha256Hex(artifactVal),
		secondURL, len("weights"), sha256Hex("weights"))
}

func registerArtifact(e *testEnv, body string) {
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"), func(req *http.Request) (*http.Response, error) {
		return artifactResponse(req, body), nil
	})
}

func artifactResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        make(http.Header),
		Request:       req,
	}
}

// downloadArgs builds the standard invocation into dir.
func downloadArgs(dir string, extra ...string) []string {
	args := []string{"model", "download", "m_ab12cd", "-R", "zetic/whisper",
		"--target", "tm_71", "--output", dir}
	return append(args, extra...)
}

// ---------------------------------------------------------------------------
// confirmation matrix
// ---------------------------------------------------------------------------

func TestDownloadNonTTYWithoutYesExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, downloadArgs(t.TempDir())...)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--yes", "the error must name the exact flag remediation")
	assert.Empty(t, e.reg.Requests, "nothing may be charged without confirmation")
}

func TestDownloadNoInputWithoutYesExits2(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true) // interactive terminal, but --no-input wins

	err := run(t, e, append([]string{"--no-input"}, downloadArgs(t.TempDir())...)...)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestDownloadNonTTYWithYes(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("sha256:"+sha256Hex(artifactVal))))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs(dir, "--yes")...))
	e.reg.Verify(t)

	require.Len(t, e.reg.Requests, 2, "--yes must skip the free preview call")
	assert.NotEmpty(t, e.reg.Requests[0].Header.Get("Idempotency-Key"),
		"the billable POST must carry an Idempotency-Key")
	assert.Empty(t, e.reg.Requests[1].Header.Get("Authorization"),
		"signed-URL downloads must never carry the PAT")

	data, err := os.ReadFile(filepath.Join(dir, "model.bin"))
	require.NoError(t, err)
	assert.Equal(t, artifactVal, string(data))
	assert.Contains(t, e.errOut.String(), "✓ Downloaded 1 artifact(s)")
	assert.Empty(t, e.out.String(), "stdout stays clean without --json")
}

func TestDownloadTTYConfirmYes(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	e.f.IOStreams.SetStdinTTY(true)
	e.in.WriteString("y\n")
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/m_ab12cd/targets"),
		jsonStub(200, targetsBody()))
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs(dir)...))
	e.reg.Verify(t)

	assert.Contains(t, e.errOut.String(), "counts against your bandwidth quota",
		"the preview must warn that the download is billable")
	assert.Contains(t, e.errOut.String(), "tm_71")
	_, err := os.Stat(filepath.Join(dir, "model.bin"))
	require.NoError(t, err)
}

func TestDownloadTTYConfirmNoNeverCharges(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	e.in.WriteString("n\n")
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/m_ab12cd/targets"),
		jsonStub(200, targetsBody()))

	err := run(t, e, downloadArgs(t.TempDir())...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	require.Len(t, e.reg.Requests, 1, "declining must stop before the billable POST")
	assert.Equal(t, "GET", e.reg.Requests[0].Method)
	assert.Contains(t, e.errOut.String(), "nothing was charged")
}

func TestDownloadTTYUnknownTargetFailsBeforeCharge(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/m_ab12cd/targets"),
		jsonStub(200, `{"results":[],"count":0}`))

	err := run(t, e, downloadArgs(t.TempDir())...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "tm_71")
	require.Len(t, e.reg.Requests, 1, "an unknown target must never reach the billable POST")
}

// ---------------------------------------------------------------------------
// idempotency across retries
// ---------------------------------------------------------------------------

func TestDownloadIdempotencyKeyStableAcross502Retry(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(502, `{}`))
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(200, authBody("")))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs(dir, "--yes")...))
	e.reg.Verify(t)

	require.GreaterOrEqual(t, len(e.reg.Requests), 3)
	key := e.reg.Requests[0].Header.Get("Idempotency-Key")
	require.NotEmpty(t, key)
	assert.Equal(t, key, e.reg.Requests[1].Header.Get("Idempotency-Key"),
		"the 502 retry must replay the SAME key so the server never double-charges")

	_, err := os.Stat(filepath.Join(dir, "model.bin"))
	require.NoError(t, err, "a 200 replay must download like a fresh 201")
}

func TestDownloadReusesPersistedKeyAfterFailureInAnotherProcess(t *testing.T) {
	var mu sync.Mutex
	var authorizationKeys []string
	artifactCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == authPath:
			authorizationKeys = append(authorizationKeys, r.Header.Get("Idempotency-Key"))
			status := http.StatusCreated
			if len(authorizationKeys) > 1 {
				status = http.StatusOK
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			artifactURL := "http://" + r.Host + "/artifact?X-Goog-Signature=PROCESSSECRET"
			_, _ = io.WriteString(w, authBodyForURL(artifactURL, "sha256:"+sha256Hex(artifactVal)))
		case r.Method == http.MethodGet && r.URL.Path == "/artifact":
			artifactCalls++
			if artifactCalls == 1 {
				http.Error(w, "permanent storage failure", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(artifactVal)))
			_, _ = io.WriteString(w, artifactVal)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	bin := buildMelangeBinary(t)

	stateHome := t.TempDir()
	destDir := t.TempDir()
	runProcess := func() (string, error) {
		cmd := exec.Command(bin, downloadArgs(destDir, "--yes")...)
		cmd.Env = append(os.Environ(),
			"MELANGE_HOST="+srv.URL,
			"MELANGE_API_KEY=ztp_process_test",
			"XDG_STATE_HOME="+stateHome,
			"MELANGE_DEBUG=",
			"NO_COLOR=1",
		)
		out, runErr := cmd.CombinedOutput()
		return string(out), runErr
	}

	firstOutput, err := runProcess()
	require.Error(t, err, "the first process must fail after authorization")
	assert.NotContains(t, firstOutput, "PROCESSSECRET")
	assert.NotContains(t, firstOutput, "/artifact?")
	stateFiles, err := filepath.Glob(filepath.Join(stateHome, "melange", "downloads", "*.json"))
	require.NoError(t, err)
	require.Len(t, stateFiles, 1, "post-authorization failure must retain replay state")
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(stateFiles[0])
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		dirInfo, statErr := os.Stat(filepath.Dir(stateFiles[0]))
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	}
	rawState, err := os.ReadFile(stateFiles[0])
	require.NoError(t, err)
	assert.NotContains(t, string(rawState), "PROCESSSECRET")
	assert.NotContains(t, string(rawState), "ztp_process_test")

	secondOutput, err := runProcess()
	require.NoError(t, err, secondOutput)
	data, err := os.ReadFile(filepath.Join(destDir, "model.bin"))
	require.NoError(t, err)
	assert.Equal(t, artifactVal, string(data))
	stateFiles, err = filepath.Glob(filepath.Join(stateHome, "melange", "downloads", "*.json"))
	require.NoError(t, err)
	require.Len(t, stateFiles, 1, "successful settlement retains a completed concurrency tombstone")
	completedState, err := os.ReadFile(stateFiles[0])
	require.NoError(t, err)
	assert.Contains(t, string(completedState), `"status": "completed"`)
	markers, err := filepath.Glob(filepath.Join(stateHome, "melange", "downloads", "*.attempts", "*.json"))
	require.NoError(t, err)
	assert.Empty(t, markers, "successful processes must clear active ownership registrations")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, authorizationKeys, 2, "each process asks for authorization URLs")
	require.NotEmpty(t, authorizationKeys[0])
	assert.Equal(t, authorizationKeys[0], authorizationKeys[1],
		"both processes must use one logical authorization key, so replay cannot recharge")
}

func TestDownloadConcurrentProcessFailureRetryNeverRotatesChargedKey(t *testing.T) {
	var mu sync.Mutex
	var authorizationKeys []string
	firstArtifactStarted := make(chan struct{})
	releaseFirstArtifact := make(chan struct{})
	var firstOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == authPath:
			mu.Lock()
			authorizationKeys = append(authorizationKeys, r.Header.Get("Idempotency-Key"))
			index := len(authorizationKeys)
			mu.Unlock()
			status := http.StatusOK
			if index == 1 {
				status = http.StatusCreated
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			artifactURL := fmt.Sprintf("http://%s/concurrent-artifact/%d?X-Goog-Signature=CONCURRENTSECRET", r.Host, index)
			_, _ = io.WriteString(w, authBodyForURL(artifactURL, "sha256:"+sha256Hex(artifactVal)))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/concurrent-artifact/"):
			switch filepath.Base(r.URL.Path) {
			case "1":
				firstOnce.Do(func() { close(firstArtifactStarted) })
				<-releaseFirstArtifact
				w.Header().Set("Content-Length", fmt.Sprint(len(artifactVal)))
				_, _ = io.WriteString(w, artifactVal)
			case "2":
				http.Error(w, "permanent follower failure", http.StatusBadRequest)
			case "3":
				w.Header().Set("Content-Length", fmt.Sprint(len(artifactVal)))
				_, _ = io.WriteString(w, artifactVal)
			default:
				http.Error(w, "unexpected authorization", http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	bin := buildMelangeBinary(t)
	stateHome := t.TempDir()
	firstDest := t.TempDir()
	secondDest := t.TempDir()
	newProcess := func(dest string) (*exec.Cmd, *bytes.Buffer) {
		var output bytes.Buffer
		cmd := exec.Command(bin, downloadArgs(dest, "--yes")...)
		cmd.Env = downloadProcessEnv(srv.URL, stateHome)
		cmd.Stdout = &output
		cmd.Stderr = &output
		return cmd, &output
	}

	first, firstOutput := newProcess(firstDest)
	require.NoError(t, first.Start())
	select {
	case <-firstArtifactStarted:
	case <-time.After(5 * time.Second):
		close(releaseFirstArtifact)
		_ = first.Wait()
		t.Fatal("first process never reached artifact transfer")
	}

	second, secondOutput := newProcess(secondDest)
	require.NoError(t, second.Start())
	markersReady := waitForAttemptMarkers(stateHome, 2, 5*time.Second)
	close(releaseFirstArtifact)
	require.True(t, markersReady, "both concurrent processes must register ownership before serialization")
	require.NoError(t, first.Wait(), firstOutput.String())
	require.Error(t, second.Wait(), "the serialized follower intentionally fails after authorization")
	assert.NotContains(t, secondOutput.String(), "CONCURRENTSECRET")

	retry, retryOutput := newProcess(secondDest)
	require.NoError(t, retry.Run(), retryOutput.String())
	data, err := os.ReadFile(filepath.Join(secondDest, "model.bin"))
	require.NoError(t, err)
	assert.Equal(t, artifactVal, string(data))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, authorizationKeys, 3)
	require.NotEmpty(t, authorizationKeys[0])
	assert.Equal(t, authorizationKeys[0], authorizationKeys[1])
	assert.Equal(t, authorizationKeys[0], authorizationKeys[2],
		"a failed concurrent follower's later process must retain the completed authorization key")
}

func buildMelangeBinary(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	bin := filepath.Join(t.TempDir(), "melange")
	build := exec.Command("go", "build", "-o", bin, "./cmd/melange")
	build.Dir = repoRoot
	buildOutput, err := build.CombinedOutput()
	require.NoError(t, err, string(buildOutput))
	return bin
}

func downloadProcessEnv(host, stateHome string) []string {
	return append(os.Environ(),
		"MELANGE_HOST="+host,
		"MELANGE_API_KEY=ztp_process_test",
		"XDG_STATE_HOME="+stateHome,
		"MELANGE_DEBUG=",
		"NO_COLOR=1",
	)
}

func waitForAttemptMarkers(stateHome string, want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		markers, _ := filepath.Glob(filepath.Join(stateHome, "melange", "downloads", "*.attempts", "*.json"))
		if len(markers) >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestDownloadDirectoryCollisionKeepsReplayKeyForForceRemediation(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.bin"), []byte("existing"), 0o644))
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(200, authBody("")))
	registerArtifact(e, artifactVal)

	err := run(t, e, downloadArgs(dir, "--yes")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")
	assert.Contains(t, err.Error(), "without another charge")

	require.NoError(t, run(t, e, downloadArgs(dir, "--yes", "--force")...))
	var keys []string
	for _, req := range e.reg.Requests {
		if req.Method == http.MethodPost {
			keys = append(keys, req.Header.Get("Idempotency-Key"))
		}
	}
	require.Len(t, keys, 2)
	assert.Equal(t, keys[0], keys[1])
}

func TestDownloadMultiArtifactOutputCorrectionReusesChargedKey(t *testing.T) {
	e := setup(t)
	output := filepath.Join(t.TempDir(), "models")
	body := twoArtifactAuthBody(
		"https://storage.googleapis.com/dl/model.bin?X-Goog-Signature=ONE",
		"https://storage.googleapis.com/dl/weights.bin?X-Goog-Signature=TWO")
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, body))
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(200, body))

	err := run(t, e, downloadArgs(output, "--yes")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "existing directory")
	require.NoError(t, os.Mkdir(output, 0o755))
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"), func(req *http.Request) (*http.Response, error) {
		return artifactResponse(req, artifactVal), nil
	})
	e.reg.Register(httpmock.REST("GET", "/dl/weights.bin"), func(req *http.Request) (*http.Response, error) {
		return artifactResponse(req, "weights"), nil
	})

	require.NoError(t, run(t, e, downloadArgs(output, "--yes")...))
	keys := authorizationRequestKeys(e.reg.Requests)
	require.Len(t, keys, 2)
	assert.Equal(t, keys[0], keys[1],
		"correcting post-authorization artifact shape must replay instead of recharge")
}

func TestDownloadStdoutShapeCorrectionReusesChargedKey(t *testing.T) {
	e := setup(t)
	body := twoArtifactAuthBody(
		"https://storage.googleapis.com/dl/model.bin?X-Goog-Signature=ONE",
		"https://storage.googleapis.com/dl/weights.bin?X-Goog-Signature=TWO")
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, body))
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(200, body))

	err := run(t, e, downloadArgs("-", "--yes")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one artifact")
	dir := t.TempDir()
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"), func(req *http.Request) (*http.Response, error) {
		return artifactResponse(req, artifactVal), nil
	})
	e.reg.Register(httpmock.REST("GET", "/dl/weights.bin"), func(req *http.Request) (*http.Response, error) {
		return artifactResponse(req, "weights"), nil
	})

	require.NoError(t, run(t, e, downloadArgs(dir, "--yes")...))
	keys := authorizationRequestKeys(e.reg.Requests)
	require.Len(t, keys, 2)
	assert.Equal(t, keys[0], keys[1],
		"changing an impossible stdout shape to a directory must keep the charged key")
}

func authorizationRequestKeys(requests []*http.Request) []string {
	var keys []string
	for _, req := range requests {
		if req.Method == http.MethodPost && req.URL.Path == authPath {
			keys = append(keys, req.Header.Get("Idempotency-Key"))
		}
	}
	return keys
}

func TestDownloadRetriesTransientArtifactGETsWithReplayableDestination(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("sha256:"+sha256Hex(artifactVal))))
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"), httpmock.ErrorResponse(syscall.ECONNRESET))
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"),
		httpmock.WithHeader(httpmock.StatusStringResponse(http.StatusTooManyRequests, "slow down"), "Retry-After", "0"))
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"), httpmock.StatusStringResponse(http.StatusBadGateway, "gateway"))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs(dir, "--yes")...))
	data, err := os.ReadFile(filepath.Join(dir, "model.bin"))
	require.NoError(t, err)
	assert.Equal(t, artifactVal, string(data))
	getCount := 0
	for _, req := range e.reg.Requests {
		if req.Method == http.MethodGet && req.URL.Path == "/dl/model.bin" {
			getCount++
		}
	}
	assert.Equal(t, 4, getCount)
}

func TestDownloadRefreshesExpiredSignedURLWithPersistedKey(t *testing.T) {
	for _, expiredStatus := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(expiredStatus), func(t *testing.T) {
			e := setup(t)
			dir := t.TempDir()
			expiredURL := "https://storage.googleapis.com/dl/expired.bin?X-Goog-Signature=EXPIREDSECRET"
			freshURL := "https://storage.googleapis.com/dl/fresh.bin?X-Goog-Signature=FRESHSECRET"
			e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBodyForURL(expiredURL, "sha256:"+sha256Hex(artifactVal))))
			e.reg.Register(httpmock.REST("GET", "/dl/expired.bin"), httpmock.StatusStringResponse(expiredStatus, "expired"))
			e.reg.Register(httpmock.REST("POST", authPath), jsonStub(200, authBodyForURL(freshURL, "sha256:"+sha256Hex(artifactVal))))
			e.reg.Register(httpmock.REST("GET", "/dl/fresh.bin"), func(req *http.Request) (*http.Response, error) {
				return artifactResponse(req, artifactVal), nil
			})

			require.NoError(t, run(t, e, downloadArgs(dir, "--yes")...))
			var keys []string
			for _, req := range e.reg.Requests {
				if req.Method == http.MethodPost {
					keys = append(keys, req.Header.Get("Idempotency-Key"))
				}
			}
			require.Len(t, keys, 2)
			assert.Equal(t, keys[0], keys[1], "URL refresh must replay, never recharge")
			assert.NotContains(t, e.errOut.String(), "EXPIREDSECRET")
			assert.NotContains(t, e.errOut.String(), "FRESHSECRET")
		})
	}
}

func TestDownloadBoundsExpiredURLRefreshAndRedactsFailure(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	firstURL := "https://storage.googleapis.com/dl/first.bin?X-Goog-Signature=FIRSTSECRET"
	secondURL := "https://storage.googleapis.com/dl/second.bin?X-Goog-Signature=SECONDSECRET"
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBodyForURL(firstURL, "")))
	e.reg.Register(httpmock.REST("GET", "/dl/first.bin"), httpmock.StatusStringResponse(403, "expired"))
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(200, authBodyForURL(secondURL, "")))
	e.reg.Register(httpmock.REST("GET", "/dl/second.bin"), httpmock.StatusStringResponse(403, "still expired"))

	err := run(t, e, downloadArgs(dir, "--yes")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replay state was kept")
	assert.NotContains(t, err.Error(), "FIRSTSECRET")
	assert.NotContains(t, err.Error(), "SECONDSECRET")
	posts := 0
	for _, req := range e.reg.Requests {
		if req.Method == http.MethodPost {
			posts++
		}
	}
	assert.Equal(t, 2, posts, "expired URL refresh must be bounded to one replay")
}

// ---------------------------------------------------------------------------
// quota
// ---------------------------------------------------------------------------

func TestDownloadQuota429Exits1WithRetryAfter(t *testing.T) {
	e := setup(t)
	quota := `{"type":"error","error":{"type":"rate_limit_error","message":"bandwidth quota exceeded"},"request_id":"req_q"}`
	e.reg.Register(httpmock.REST("POST", authPath),
		httpmock.WithHeader(jsonStub(429, quota), "Retry-After", "1"))

	err := run(t, e, downloadArgs(t.TempDir(), "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	require.Len(t, e.reg.Requests, 1,
		"a quota 429 is not transient at retry timescales; the billable POST must surface it immediately")
	assert.Contains(t, err.Error(), "bandwidth quota exceeded", "the server quota message must surface")
	assert.Contains(t, err.Error(), "Retry after 1s", "Retry-After must surface when present")
	assert.Contains(t, err.Error(), "nothing was charged")
}

// ---------------------------------------------------------------------------
// checksum verification
// ---------------------------------------------------------------------------

func TestDownloadVerifiesCRC32CChecksum(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("crc32c:"+crc32cB64(artifactVal))))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs(dir, "--yes")...))
	_, err := os.Stat(filepath.Join(dir, "model.bin"))
	require.NoError(t, err)
	assert.NotContains(t, e.errOut.String(), "unrecognized checksum")
}

func TestDownloadChecksumMismatchCleansUp(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("sha256:"+sha256Hex("other bytes"))))
	registerArtifact(e, artifactVal)

	err := run(t, e, downloadArgs(dir, "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "checksum mismatch")

	entries, rerr := os.ReadDir(dir)
	require.NoError(t, rerr)
	assert.Empty(t, entries, "no final file and no temp file may survive a checksum mismatch")
}

func TestDownloadUnrecognizedChecksumWarnsButSucceeds(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("md5:abcdef")))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs(dir, "--yes")...))
	assert.Contains(t, e.errOut.String(), "unrecognized checksum format")
	data, err := os.ReadFile(filepath.Join(dir, "model.bin"))
	require.NoError(t, err)
	assert.Equal(t, artifactVal, string(data))
}

// ---------------------------------------------------------------------------
// atomicity and overwrite policy
// ---------------------------------------------------------------------------

func TestDownloadFailedTransferLeavesNoPartialFile(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"),
		httpmock.StatusStringResponse(403, "expired"))

	err := run(t, e, downloadArgs(dir, "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.NotContains(t, err.Error(), "SECRETSIG", "errors must never leak the signed URL")

	entries, rerr := os.ReadDir(dir)
	require.NoError(t, rerr)
	assert.Empty(t, entries, "a failed transfer must leave no partial or temp file")
}

func TestDownloadRefusesOverwriteWithoutForce(t *testing.T) {
	e := setup(t)
	dest := filepath.Join(t.TempDir(), "model.bin")
	require.NoError(t, os.WriteFile(dest, []byte("existing"), 0o644))

	err := run(t, e, downloadArgs(dest, "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err), "an existing --output file is a usage error")
	assert.Contains(t, err.Error(), "--force")
	assert.Empty(t, e.reg.Requests,
		"a refused overwrite is knowable up front and must never cost quota")

	data, rerr := os.ReadFile(dest)
	require.NoError(t, rerr)
	assert.Equal(t, "existing", string(data), "the existing file must be untouched")
}

func TestDownloadDirModeRefusesOverwriteWithoutForce(t *testing.T) {
	// Dir-mode collisions are only knowable after the billable POST (artifact
	// names come from the authorization), so this refusal is post-charge but
	// must still land before any bytes move.
	e := setup(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.bin"), []byte("existing"), 0o644))
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))

	err := run(t, e, downloadArgs(dir, "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--force")
	require.Len(t, e.reg.Requests, 1, "the refusal must land before any artifact GET")

	data, rerr := os.ReadFile(filepath.Join(dir, "model.bin"))
	require.NoError(t, rerr)
	assert.Equal(t, "existing", string(data), "the existing file must be untouched")
}

func TestDownloadForceOverwrites(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.bin"), []byte("existing"), 0o644))
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs(dir, "--yes", "--force")...))
	data, err := os.ReadFile(filepath.Join(dir, "model.bin"))
	require.NoError(t, err)
	assert.Equal(t, artifactVal, string(data))
}

func TestDownloadNoForceCommitDoesNotReplaceRacingFile(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "model.bin")
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("sha256:"+sha256Hex(artifactVal))))
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"), func(_ *http.Request) (*http.Response, error) {
		// The path did not exist during preflight. Simulate another process
		// winning the race before the atomic publication step.
		require.NoError(t, os.WriteFile(dest, []byte("racer"), 0o644))
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(artifactVal)),
			ContentLength: int64(len(artifactVal)),
			Header:        make(http.Header),
		}, nil
	})

	err := run(t, e, downloadArgs(dir, "--yes")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "without another charge")
	data, readErr := os.ReadFile(dest)
	require.NoError(t, readErr)
	assert.Equal(t, "racer", string(data), "a racing destination must never be replaced without --force")
}

func TestDownloadOutputDashWritesOnlyVerifiedBinaryToStdout(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("sha256:"+sha256Hex(artifactVal))))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs("-", "--yes")...))
	assert.Equal(t, artifactVal, e.out.String())
	assert.Contains(t, e.errOut.String(), "Downloaded 1 artifact")
}

func TestDownloadOutputDashRejectsStructuredOutputBeforeCharge(t *testing.T) {
	e := setup(t)
	err := run(t, e, downloadArgs("-", "--yes", "--json")...)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "cannot be combined")
	assert.Empty(t, e.reg.Requests)
}

func TestDownloadRejectsAuthorizedSizeMismatch(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	auth := fmt.Sprintf(`{"authorization_id":"da_1","expires_at":"2026-07-21T12:00:00Z",`+
		`"artifacts":[{"name":"model.bin","url":%q,"size":999}]}`, signedURL)
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, auth))
	registerArtifact(e, artifactVal)

	err := run(t, e, downloadArgs(dir, "--yes")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authorized size")
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestDownloadRejectsUnknownChecksumWhenSizeIsUnknowable(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	auth := fmt.Sprintf(`{"authorization_id":"da_1","expires_at":"2026-07-21T12:00:00Z",`+
		`"artifacts":[{"name":"model.bin","url":%q,"checksum":"md5:abcdef"}]}`, signedURL)
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, auth))
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"), func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewBufferString(artifactVal)),
			ContentLength: -1,
			Header:        make(http.Header),
		}, nil
	})

	err := run(t, e, downloadArgs(dir, "--yes")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot verify completeness")
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

// ---------------------------------------------------------------------------
// transport failures must never leak the signed URL
// ---------------------------------------------------------------------------

func TestDownloadTransportErrorNeverLeaksSignedURL(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))
	// A DNS/conn/TLS failure surfaces as a *url.Error whose Error() embeds
	// the full signed URL — the command must strip it before stderr.
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"),
		httpmock.ErrorResponse(errors.New("connect: connection refused")))

	err := run(t, e, downloadArgs(t.TempDir(), "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "model.bin", "the error must name the failing artifact")
	assert.Contains(t, err.Error(), "connection refused", "the underlying cause must surface")
	assert.NotContains(t, err.Error(), "SECRETSIG", "transport errors must never leak the signed URL")
	assert.NotContains(t, err.Error(), "storage.googleapis.com")
	assert.NotContains(t, e.errOut.String(), "SECRETSIG")
}

func TestDownloadInterruptExits130WithoutURL(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"),
		func(req *http.Request) (*http.Response, error) {
			cancel() // simulate SIGINT mid-download
			return nil, req.Context().Err()
		})

	err := runCtx(t, ctx, e, downloadArgs(t.TempDir(), "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 130, cmdutil.ExitCode(err), "SIGINT during an artifact download must exit 130")
	assert.NotContains(t, err.Error(), "SECRETSIG", "the interrupt error must never carry the signed URL")
	assert.NotContains(t, err.Error(), "storage.googleapis.com")
	assert.NotContains(t, e.errOut.String(), "SECRETSIG")
}

func TestDownloadSingleArtifactToNamedFile(t *testing.T) {
	e := setup(t)
	dest := filepath.Join(t.TempDir(), "renamed.bin")
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs(dest, "--yes")...))
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, artifactVal, string(data))
}

func TestDownloadMultiArtifactRequiresDirectoryOutput(t *testing.T) {
	e := setup(t)
	dest := filepath.Join(t.TempDir(), "single.bin")
	multi := `{"authorization_id":"da_1","expires_at":"2026-07-21T12:00:00Z","artifacts":[` +
		fmt.Sprintf(`{"name":"a.bin","url":%q},{"name":"b.bin","url":%q}]}`, signedURL, signedURL)
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, multi))

	err := run(t, e, downloadArgs(dest, "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "directory")
}

func TestDownloadRejectsUnsafeArtifactNames(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	evil := fmt.Sprintf(`{"authorization_id":"da_1","expires_at":"2026-07-21T12:00:00Z",`+
		`"artifacts":[{"name":"../escape.bin","url":%q}]}`, signedURL)
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, evil))

	err := run(t, e, downloadArgs(dir, "--yes")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe artifact name")
	_, serr := os.Stat(filepath.Join(filepath.Dir(dir), "escape.bin"))
	assert.True(t, os.IsNotExist(serr))
}

func TestDownloadRejectsCrossPlatformReservedArtifactNames(t *testing.T) {
	for _, name := range []string{"CON", "aux.txt", "LPT1.bin", "trailing. ", "bad:name.bin", "control\x01.bin"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			e := setup(t)
			encodedName, err := json.Marshal(name)
			require.NoError(t, err)
			auth := fmt.Sprintf(`{"authorization_id":"da_1","expires_at":"2026-07-21T12:00:00Z",`+
				`"artifacts":[{"name":%s,"url":%q,"size":1}]}`, encodedName, signedURL)
			e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, auth))

			err = run(t, e, downloadArgs(t.TempDir(), "--yes")...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsafe artifact name")
			require.Len(t, e.reg.Requests, 1, "unsafe server names must fail before artifact transfer")
		})
	}
}

func TestDownloadRejectsCaseFoldedArtifactNameCollisions(t *testing.T) {
	e := setup(t)
	auth := fmt.Sprintf(`{"authorization_id":"da_1","expires_at":"2026-07-21T12:00:00Z","artifacts":[`+
		`{"name":"Model.bin","url":%q,"size":1},{"name":"model.BIN","url":%q,"size":1}]}`,
		signedURL, signedURL)
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, auth))

	err := run(t, e, downloadArgs(t.TempDir(), "--yes")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collide on case-insensitive filesystems")
	require.Len(t, e.reg.Requests, 1)
}

// ---------------------------------------------------------------------------
// --json redaction
// ---------------------------------------------------------------------------

func TestDownloadJSONRedactsSignedURLs(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs(dir, "--yes", "--json")...))

	out := e.out.String()
	assert.Contains(t, out, `"authorization_id":"da_1"`)
	assert.Contains(t, out, `"url":"<redacted>"`)
	assert.NotContains(t, out, "SECRETSIG", "the signed URL must never appear in --json output")
	assert.NotContains(t, out, "storage.googleapis.com")

	_, err := os.Stat(filepath.Join(dir, "model.bin"))
	require.NoError(t, err, "--json still downloads the files")
}

func TestDownloadJQOnRedactedAuthorization(t *testing.T) {
	e := setup(t)
	dir := t.TempDir()
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))
	registerArtifact(e, artifactVal)

	require.NoError(t, run(t, e, downloadArgs(dir, "--yes", "--jq", ".artifacts[].name")...))
	assert.Equal(t, "model.bin\n", e.out.String())
}

// ---------------------------------------------------------------------------
// flag validation and auth mapping
// ---------------------------------------------------------------------------

func TestDownloadMissingTargetExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "model", "download", "m_ab12cd", "-R", "zetic/whisper", "--yes")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--target")
	assert.Empty(t, e.reg.Requests)
}

func TestDownloadBadOutputFailsBeforeCharge(t *testing.T) {
	e := setup(t)
	dest := filepath.Join(t.TempDir(), "missing-parent", "file.bin")

	err := run(t, e, downloadArgs(dest, "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "parent directory")
	assert.Empty(t, e.reg.Requests, "a bad --output must never cost quota")
}

func TestDownloadUnwritableDestinationFailsBeforeCharge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not model Windows ACL writability")
	}
	e := setup(t)
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o500))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	dest := filepath.Join(parent, "model.bin")

	err := run(t, e, downloadArgs(dest, "--yes")...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not writable")
	assert.Empty(t, e.reg.Requests, "destination writability must be proven before authorization can charge")
}

func TestDownloadMissingRepoExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "model", "download", "m_ab12cd", "--target", "tm_71", "--yes")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestDownloadNotFoundExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(404, notFound))

	err := run(t, e, downloadArgs(t.TempDir(), "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
}

func TestDownloadUnauthenticatedExits4(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(401, badAuth))

	err := run(t, e, downloadArgs(t.TempDir(), "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))
}
