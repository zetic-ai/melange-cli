package upload

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// White-box tests for the GOOS-parameterized state directory choice: there is
// no Windows CI leg, so the Windows branch is exercised by injecting the GOOS.

func TestStateDirForWindowsUsesLocalAppData(t *testing.T) {
	t.Setenv("LOCALAPPDATA", filepath.Join("C:", "Users", "dev", "AppData", "Local"))
	t.Setenv("XDG_STATE_HOME", "/should/not/be/used")

	dir, err := stateDirFor("windows")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("C:", "Users", "dev", "AppData", "Local", "melange", "uploads"), dir)
}

func TestStateDirForWindowsWithoutLocalAppDataFallsThrough(t *testing.T) {
	t.Setenv("LOCALAPPDATA", "")
	t.Setenv("XDG_STATE_HOME", filepath.Join("/", "tmp", "xdg-state"))

	dir, err := stateDirFor("windows")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/", "tmp", "xdg-state", "melange", "uploads"), dir)
}

func TestStateDirForUnixIgnoresLocalAppData(t *testing.T) {
	t.Setenv("LOCALAPPDATA", filepath.Join("C:", "Users", "dev", "AppData", "Local"))
	t.Setenv("XDG_STATE_HOME", filepath.Join("/", "tmp", "xdg-state"))

	for _, goos := range []string{"linux", "darwin"} {
		dir, err := stateDirFor(goos)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join("/", "tmp", "xdg-state", "melange", "uploads"), dir, goos)
	}
}

func TestStateDirForUnixDefaultsToDotLocalState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", filepath.Join("/", "home", "dev"))

	dir, err := stateDirFor("linux")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/", "home", "dev", ".local", "state", "melange", "uploads"), dir)
}
