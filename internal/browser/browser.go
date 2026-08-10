package browser

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

// ErrNoDisplay is returned when no graphical display is available.
var ErrNoDisplay = errors.New("no display")

// Open opens url in the default browser. On Linux it pre-checks DISPLAY.
func Open(url string) error {
	if runtime.GOOS == "linux" {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return ErrNoDisplay
		}
	}
	cmd := commandForOS(runtime.GOOS, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

func commandForOS(goos, url string) *exec.Cmd {
	switch goos {
	case "darwin":
		return exec.Command("open", url)
	case "windows":
		return exec.Command("cmd", "/c", "start", url)
	default:
		return exec.Command("xdg-open", url)
	}
}
