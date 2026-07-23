package upload_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/upload"
)

func TestSessionLockSerializesAcrossProcesses(t *testing.T) {
	if os.Getenv("MELANGE_UPLOAD_LOCK_HELPER") == "1" {
		runSessionLockHelper(t)
		return
	}

	stateHome := t.TempDir()
	releaseOne := filepath.Join(t.TempDir(), "release-one")
	readyOne := filepath.Join(t.TempDir(), "ready-one")
	releaseTwo := filepath.Join(t.TempDir(), "release-two")
	readyTwo := filepath.Join(t.TempDir(), "ready-two")

	first := lockHelperCommand(t, stateHome, "up_shared", readyOne, releaseOne)
	require.NoError(t, first.Start())
	waitForFile(t, readyOne)

	second := lockHelperCommand(t, stateHome, "up_shared", readyTwo, releaseTwo)
	require.NoError(t, second.Start())
	time.Sleep(150 * time.Millisecond)
	_, err := os.Stat(readyTwo)
	require.ErrorIs(t, err, os.ErrNotExist, "second process must wait for the same session lock")

	require.NoError(t, os.WriteFile(releaseOne, []byte("release"), 0o600))
	require.NoError(t, first.Wait())
	waitForFile(t, readyTwo)
	require.NoError(t, os.WriteFile(releaseTwo, []byte("release"), 0o600))
	require.NoError(t, second.Wait())
}

func TestAcquireSessionRejectsPreCanceledContext(t *testing.T) {
	stateEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lease, err := upload.AcquireSession(ctx, "up_canceled")
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, lease)
}

func lockHelperCommand(t *testing.T, stateHome, sessionID, ready, release string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestSessionLockSerializesAcrossProcesses")
	cmd.Env = append(os.Environ(),
		"MELANGE_UPLOAD_LOCK_HELPER=1",
		"XDG_STATE_HOME="+stateHome,
		"MELANGE_UPLOAD_LOCK_SESSION="+sessionID,
		"MELANGE_UPLOAD_LOCK_READY="+ready,
		"MELANGE_UPLOAD_LOCK_RELEASE="+release,
	)
	return cmd
}

func runSessionLockHelper(t *testing.T) {
	t.Helper()
	lease, err := upload.AcquireSession(context.Background(), os.Getenv("MELANGE_UPLOAD_LOCK_SESSION"))
	require.NoError(t, err)
	defer lease.Close() //nolint:errcheck
	require.NoError(t, os.WriteFile(os.Getenv("MELANGE_UPLOAD_LOCK_READY"), []byte("ready"), 0o600))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("MELANGE_UPLOAD_LOCK_RELEASE")); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			require.NoError(t, err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for parent release")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			require.NoError(t, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStateConcurrentSavesUseIndependentAtomicTemps(t *testing.T) {
	stateEnv(t)
	st := sampleState()
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- st.Save()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}

	got, err := upload.LoadState(st.SessionID)
	require.NoError(t, err)
	assert.Equal(t, st.SessionID, got.SessionID)
	dir, err := upload.StateDir()
	require.NoError(t, err)
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	require.NoError(t, err)
	hiddenMatches, err := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	require.NoError(t, err)
	matches = append(matches, hiddenMatches...)
	assert.Empty(t, matches, "atomic temporary files must be cleaned up")
}
