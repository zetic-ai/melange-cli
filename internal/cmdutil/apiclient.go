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

// DebugEnabled reports whether MELANGE_DEBUG asks for verbose diagnostics on
// stderr. It is the single source of truth for the debug switch, shared by the
// API client's request logging and the MCP server's diagnostic logger.
func DebugEnabled() bool {
	return envTruthy(os.Getenv("MELANGE_DEBUG"))
}

// UserAgent is the User-Agent every outgoing Melange API request carries,
// whatever builds the client — the CLI's own commands or the MCP HTTP
// server's per-request clients. One function so the API only ever sees one
// shape of this string.
func UserAgent(version string) string {
	return fmt.Sprintf("melange-cli/%s (%s; %s)", version, runtime.GOOS, runtime.GOARCH)
}

// APITimeout resolves the per-request API timeout from MELANGE_API_TIMEOUT,
// falling back to api.DefaultRequestTimeout. A set-but-unparsable value is a
// hard error rather than a silent fallback: an operator who asked for a
// specific timeout must not get a different one.
func APITimeout() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("MELANGE_API_TIMEOUT"))
	if raw == "" {
		return api.DefaultRequestTimeout, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("MELANGE_API_TIMEOUT must be a positive duration (for example 30s or 2m), got %q", raw)
	}
	return parsed, nil
}

// NewAPIClient builds an api.Client for the given host and token, honoring
// the factory's base transport override and a truthy MELANGE_DEBUG (debug
// lines go to stderr). token may be empty for unauthenticated clients.
func NewAPIClient(f *Factory, host, token string) (*api.Client, error) {
	var debug io.Writer
	if DebugEnabled() {
		debug = f.IOStreams.ErrOut
	}
	timeout, err := APITimeout()
	if err != nil {
		return nil, err
	}
	return api.NewClient(api.Options{
		Host:      host,
		Token:     token,
		UserAgent: UserAgent(f.Version),
		Debug:     debug,
		Transport: f.HTTPTransport,
		Timeout:   timeout,
	})
}
