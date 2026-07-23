package upload_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/upload"
)

func stateEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("LOCALAPPDATA", dir)
	return dir
}

func sampleState() *upload.State {
	return &upload.State{
		SessionID: "up_abc123",
		Repo:      "zetic/whisper-tiny",
		Tag:       "zt_9f",
		CreatedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		Files: []*upload.StateFile{{
			ClientFileID:  "f0",
			LocalPath:     "/tmp/model.onnx",
			CanonicalPath: "zt_9f/model.onnx",
			UploadURL:     "https://storage.googleapis.com/b/o?X-Goog-Signature=sig",
			SessionURI:    "https://storage.googleapis.com/b/o?upload_id=SECRET",
			Size:          42,
			CRC32C:        "yZRlqg==",
			Offset:        16,
		}},
	}
}

func TestStateSaveLoadRoundTrip(t *testing.T) {
	stateEnv(t)
	st := sampleState()
	require.NoError(t, st.Save())

	got, err := upload.LoadState("up_abc123")
	require.NoError(t, err)
	assert.Equal(t, st, got)
}

func TestStateFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := stateEnv(t)
	require.NoError(t, sampleState().Save())

	path := filepath.Join(dir, "melange", "uploads", "up_abc123.json")
	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "state file holds session URIs (bearer credentials)")

	di, err := os.Stat(filepath.Join(dir, "melange", "uploads"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
}

func TestLoadStateMissingIsNotExist(t *testing.T) {
	stateEnv(t)
	_, err := upload.LoadState("up_nope")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLoadStateCorruptHasRemediation(t *testing.T) {
	dir := stateEnv(t)
	path := filepath.Join(dir, "melange", "uploads", "up_bad.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	_, err := upload.LoadState("up_bad")
	require.ErrorIs(t, err, upload.ErrStateCorrupt, "corruption must be distinguishable from a missing file")
	assert.NotErrorIs(t, err, os.ErrNotExist)
	msg := err.Error()
	assert.Contains(t, msg, "state file corrupt")
	assert.Contains(t, msg, "delete "+path)
	assert.Contains(t, msg, "--resume up_bad")
	assert.Contains(t, msg, "rebuild from the server")
}

func TestStateRejectsPathySessionIDs(t *testing.T) {
	stateEnv(t)
	for _, id := range []string{"", "../evil", "a/b", "a\\b", "up id"} {
		_, err := upload.LoadState(id)
		require.Error(t, err, "id %q", id)
		st := sampleState()
		st.SessionID = id
		require.Error(t, st.Save(), "id %q", id)
	}
}

func TestRemoveStateIdempotent(t *testing.T) {
	stateEnv(t)
	require.NoError(t, sampleState().Save())
	require.NoError(t, upload.RemoveState("up_abc123"))
	require.NoError(t, upload.RemoveState("up_abc123"), "removing twice is fine")
	_, err := upload.LoadState("up_abc123")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRemoveMissingStateBeforeDirectoryExistsIsNoError(t *testing.T) {
	stateEnv(t)
	require.NoError(t, upload.RemoveState("up_absent"))
}
