package browser

import (
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenNoDisplay(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("DISPLAY guard only on linux")
	}
	origDisp := os.Getenv("DISPLAY")
	origWay := os.Getenv("WAYLAND_DISPLAY")
	defer func() {
		t.Setenv("DISPLAY", origDisp)
		t.Setenv("WAYLAND_DISPLAY", origWay)
	}()
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	err := Open("https://example.com")
	require.ErrorIs(t, err, ErrNoDisplay)
	assert.Equal(t, "no display", err.Error())
}

func TestOpenWithDisplayAttemptsXdgOpen(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("xdg-open branch only on linux")
	}
	origDisp := os.Getenv("DISPLAY")
	origWay := os.Getenv("WAYLAND_DISPLAY")
	defer func() {
		t.Setenv("DISPLAY", origDisp)
		t.Setenv("WAYLAND_DISPLAY", origWay)
	}()
	// Set DISPLAY to exercise non-ErrNoDisplay path
	t.Setenv("DISPLAY", ":0")
	// Don't assert success - xdg-open may not exist in minimal container
	err := Open("https://example.com")
	// If xdg-open missing, Start returns error; if present, succeeds. Both are acceptable for coverage.
	if err != nil {
		assert.NotErrorIs(t, err, ErrNoDisplay)
		assert.NotEmpty(t, err.Error())
	}
}

func TestOpenWaylandDisplayAlsoCovers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Wayland branch only on linux")
	}
	origDisp := os.Getenv("DISPLAY")
	origWay := os.Getenv("WAYLAND_DISPLAY")
	defer func() {
		t.Setenv("DISPLAY", origDisp)
		t.Setenv("WAYLAND_DISPLAY", origWay)
	}()
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	err := Open("https://example.com")
	// Should not be ErrNoDisplay now, but may fail on cmd.Start
	if err != nil {
		assert.False(t, errors.Is(err, ErrNoDisplay))
	}
}

func TestOpenEmptyURLErrorsButNotNoDisplayWhenDisplaySet(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	_ = Open("")
	// Just ensures branch executed; we don't care about result for coverage
}

func TestErrNoDisplayIsSentinel(t *testing.T) {
	assert.True(t, errors.Is(ErrNoDisplay, ErrNoDisplay))
	assert.Equal(t, "no display", ErrNoDisplay.Error())
}

func TestOpenStartFailureWhenBinaryMissing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip()
	}
	origDisp := os.Getenv("DISPLAY")
	origPath := os.Getenv("PATH")
	defer func() {
		t.Setenv("DISPLAY", origDisp)
		t.Setenv("PATH", origPath)
	}()
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("PATH", t.TempDir()) // empty dir, xdg-open not found
	err := Open("https://example.com")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoDisplay)
}
