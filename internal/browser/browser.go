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
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}
