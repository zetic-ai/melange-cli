package model_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

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
	cs := "null"
	if checksum != "" {
		cs = fmt.Sprintf("%q", checksum)
	}
	return fmt.Sprintf(`{"authorization_id":"da_1","expires_at":"2026-07-21T12:00:00Z",`+
		`"artifacts":[{"name":"model.bin","url":%q,"size":%d,"checksum":%s}]}`,
		signedURL, len(artifactVal), cs)
}

func registerArtifact(e *testEnv, body string) {
	e.reg.Register(httpmock.REST("GET", "/dl/model.bin"),
		httpmock.StatusStringResponse(200, body))
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

// ---------------------------------------------------------------------------
// quota
// ---------------------------------------------------------------------------

func TestDownloadQuota429Exits1WithRetryAfter(t *testing.T) {
	e := setup(t)
	quota := `{"type":"error","error":{"type":"rate_limit_error","message":"bandwidth quota exceeded"},"request_id":"req_q"}`
	// The transport retries Idempotency-Keyed requests on 429 (safe: replay
	// never double-charges), so a persistent quota error takes all 4 attempts.
	for range 4 {
		e.reg.Register(httpmock.REST("POST", authPath),
			httpmock.WithHeader(jsonStub(429, quota), "Retry-After", "1"))
	}

	err := run(t, e, downloadArgs(t.TempDir(), "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
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
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.bin"), []byte("existing"), 0o644))
	e.reg.Register(httpmock.REST("POST", authPath), jsonStub(201, authBody("")))

	err := run(t, e, downloadArgs(dir, "--yes")...)
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--force")

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
