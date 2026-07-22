package cmdutil

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/zetic-ai/melange-cli/internal/api"
)

// envTruthy reports whether an environment value means "on":
// 1, true, yes, or on (case-insensitive).
func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// NewAPIClient builds an api.Client for the given host and token, honoring
// the factory's base transport override and a truthy MELANGE_DEBUG (debug
// lines go to stderr). token may be empty for unauthenticated clients.
func NewAPIClient(f *Factory, host, token string) (*api.Client, error) {
	var debug io.Writer
	if envTruthy(os.Getenv("MELANGE_DEBUG")) {
		debug = f.IOStreams.ErrOut
	}
	timeout := api.DefaultRequestTimeout
	if raw := strings.TrimSpace(os.Getenv("MELANGE_API_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("MELANGE_API_TIMEOUT must be a positive duration (for example 30s or 2m), got %q", raw)
		}
		timeout = parsed
	}
	return api.NewClient(api.Options{
		Host:      host,
		Token:     token,
		UserAgent: fmt.Sprintf("melange-cli/%s (%s; %s)", f.Version, runtime.GOOS, runtime.GOARCH),
		Debug:     debug,
		Transport: f.HTTPTransport,
		Timeout:   timeout,
	})
}
