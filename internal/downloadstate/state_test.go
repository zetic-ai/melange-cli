package downloadstate_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/downloadstate"
)

func testIdentity() downloadstate.Identity {
	return downloadstate.Identity{
		Host:    "https://api.zetic.ai",
		Account: "zetic",
		Repo:    "whisper",
		Model:   "m_ab12cd",
		Target:  "tm_71",
	}
}

func testOutput(path string) downloadstate.Output {
	return downloadstate.Output{Mode: "directory", Path: path}
}

func setStateHome(t *testing.T, path string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", path)
	t.Setenv("LOCALAPPDATA", path)
}

func TestAcquirePersistsOneKeyForAnExactLogicalDownload(t *testing.T) {
	setStateHome(t, t.TempDir())
	id := testIdentity()
	output := testOutput(filepath.Join(t.TempDir(), "models"))

	first, err := downloadstate.Acquire(context.Background(), id, output, func() string { return "key-first" })
	require.NoError(t, err)
	assert.Equal(t, "key-first", first.State().IdempotencyKey)
	require.NoError(t, first.Close())

	second, err := downloadstate.Acquire(context.Background(), id, output, func() string { return "key-second" })
	require.NoError(t, err)
	assert.Equal(t, "key-first", second.State().IdempotencyKey,
		"a later CLI process must replay the persisted authorization key")
	require.NoError(t, second.Close())
}

func TestStateIsAtomicPrivateAndContainsNoTransferCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	base := t.TempDir()
	setStateHome(t, base)
	id := testIdentity()

	lease, err := downloadstate.Acquire(context.Background(), id, testOutput(filepath.Join(t.TempDir(), "models")), func() string { return "idem-safe" })
	require.NoError(t, err)
	require.NoError(t, lease.Close())
	path, err := downloadstate.Path(id)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "X-Goog-Signature")
	assert.NotContains(t, string(raw), "ztp_")
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasSuffix(entry.Name(), ".tmp"), "atomic save must not leave a temp file")
	}
}

func TestStateBindsServerTargetToDistinctRecords(t *testing.T) {
	setStateHome(t, t.TempDir())
	firstID := testIdentity()
	secondID := testIdentity()
	secondID.Target = "tm_other"

	first, err := downloadstate.Acquire(context.Background(), firstID, testOutput("/tmp/one"), func() string { return "key-one" })
	require.NoError(t, err)
	second, err := downloadstate.Acquire(context.Background(), secondID, testOutput("/tmp/two"), func() string { return "key-two" })
	require.NoError(t, err)
	assert.NotEqual(t, first.State().IdempotencyKey, second.State().IdempotencyKey)
	assert.NotEqual(t, mustPath(t, firstID), mustPath(t, secondID))
	require.NoError(t, first.Close())
	require.NoError(t, second.Close())
}

func TestEveryIdentityFieldSelectsADistinctReplayRecord(t *testing.T) {
	setStateHome(t, t.TempDir())
	base := testIdentity()
	mutations := map[string]func(*downloadstate.Identity){
		"host":    func(id *downloadstate.Identity) { id.Host = "https://staging.zetic.ai" },
		"account": func(id *downloadstate.Identity) { id.Account = "other" },
		"repo":    func(id *downloadstate.Identity) { id.Repo = "other-repo" },
		"model":   func(id *downloadstate.Identity) { id.Model = "m_other" },
		"target":  func(id *downloadstate.Identity) { id.Target = "tm_other" },
	}
	basePath := mustPath(t, base)
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			assert.NotEqual(t, basePath, mustPath(t, changed))
		})
	}
}

func TestCorruptOrMismatchedStateNeverSilentlyCreatesAChargeableKey(t *testing.T) {
	setStateHome(t, t.TempDir())
	id := testIdentity()
	path := mustPath(t, id)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("{not-json"), 0o600))

	generated := false
	_, err := downloadstate.Acquire(context.Background(), id, testOutput("/tmp/models"), func() string {
		generated = true
		return "new-key"
	})
	assert.ErrorIs(t, err, downloadstate.ErrStateCorrupt)
	assert.False(t, generated, "losing a previously charged key must not silently create a replacement")

	require.NoError(t, os.Remove(path))
	other := testIdentity()
	other.Repo = "other-repo"
	otherLease, err := downloadstate.Acquire(context.Background(), other, testOutput("/tmp/other"), func() string { return "other-key" })
	require.NoError(t, err)
	otherState := otherLease.State()
	require.NoError(t, otherLease.Close())
	raw, err := os.ReadFile(mustPath(t, other))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	assert.NotEmpty(t, otherState.IdempotencyKey)

	_, err = downloadstate.Acquire(context.Background(), id, testOutput("/tmp/models"), func() string { return "replacement-key" })
	assert.True(t, errors.Is(err, downloadstate.ErrStateMismatch))
}

func TestOldValidStateIsReusedInsteadOfRotatingAChargedKey(t *testing.T) {
	setStateHome(t, t.TempDir())
	id := testIdentity()
	lease, err := downloadstate.Acquire(context.Background(), id, testOutput("/tmp/models"), func() string { return "old-but-charged-key" })
	require.NoError(t, err)
	st := lease.State()
	require.NoError(t, lease.Close())
	st.CreatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mustPath(t, id), raw, 0o600))

	loaded, err := downloadstate.Acquire(context.Background(), id, testOutput("/tmp/models"), func() string { return "replacement" })
	require.NoError(t, err)
	assert.Equal(t, "old-but-charged-key", loaded.State().IdempotencyKey,
		"age alone cannot prove an authorization was uncharged or safe to replace")
	require.NoError(t, loaded.Close())
}

func TestLeaseReusesPendingKeyAcrossOutputShapeCorrections(t *testing.T) {
	setStateHome(t, t.TempDir())
	id := testIdentity()

	first, err := downloadstate.Acquire(context.Background(), id, downloadstate.Output{Mode: "stdout", Path: "-"}, func() string { return "charged-key" })
	require.NoError(t, err)
	assert.Equal(t, "charged-key", first.State().IdempotencyKey)
	require.NoError(t, first.PreserveRecovery())
	require.NoError(t, first.Close())

	second, err := downloadstate.Acquire(context.Background(), id, downloadstate.Output{Mode: "directory", Path: "/tmp/models"}, func() string { return "must-not-rotate" })
	require.NoError(t, err)
	assert.Equal(t, "charged-key", second.State().IdempotencyKey)
	require.NoError(t, second.Close())
}

func TestCompletedRecoveryTombstoneKeepsKeyButUnrelatedAttemptRotates(t *testing.T) {
	setStateHome(t, t.TempDir())
	id := testIdentity()
	output := downloadstate.Output{Mode: "directory", Path: "/tmp/models"}

	first, err := downloadstate.Acquire(context.Background(), id, output, func() string { return "completed-key" })
	require.NoError(t, err)
	require.NoError(t, first.Complete())
	require.NoError(t, first.PreserveRecovery())
	require.NoError(t, first.Close())

	recovery, err := downloadstate.Acquire(context.Background(), id, output, func() string { return "must-not-rotate" })
	require.NoError(t, err)
	assert.Equal(t, "completed-key", recovery.State().IdempotencyKey)
	require.NoError(t, recovery.Complete())
	require.NoError(t, recovery.Close())

	unrelated, err := downloadstate.Acquire(context.Background(), id, downloadstate.Output{Mode: "directory", Path: "/tmp/other"}, func() string { return "new-logical-key" })
	require.NoError(t, err)
	assert.Equal(t, "new-logical-key", unrelated.State().IdempotencyKey)
	require.NoError(t, unrelated.Close())
}

func TestCompletionTombstoneOwnsProcessesRegisteredBeforeSettlement(t *testing.T) {
	stateHome := t.TempDir()
	setStateHome(t, stateHome)
	id := testIdentity()

	leader, err := downloadstate.Acquire(context.Background(), id, testOutput("/tmp/leader"), func() string { return "shared-key" })
	require.NoError(t, err)

	type result struct {
		lease *downloadstate.Lease
		err   error
	}
	followerResult := make(chan result, 1)
	go func() {
		lease, acquireErr := downloadstate.Acquire(context.Background(), id, testOutput("/tmp/follower"), func() string { return "must-not-rotate" })
		followerResult <- result{lease: lease, err: acquireErr}
	}()

	markerPattern := filepath.Join(stateHome, "melange", "downloads", "*.attempts", "*.json")
	require.Eventually(t, func() bool {
		markers, globErr := filepath.Glob(markerPattern)
		return globErr == nil && len(markers) == 2
	}, 2*time.Second, 10*time.Millisecond,
		"the follower must durably register before it waits for the transfer lock")

	require.NoError(t, leader.Complete())
	require.NoError(t, leader.Close())
	follower := <-followerResult
	require.NoError(t, follower.err)
	assert.Equal(t, "shared-key", follower.lease.State().IdempotencyKey,
		"a process registered before settlement must be named by the tombstone")
	require.NoError(t, follower.lease.Close())
}

func mustPath(t *testing.T, id downloadstate.Identity) string {
	t.Helper()
	path, err := downloadstate.Path(id)
	require.NoError(t, err)
	return path
}
