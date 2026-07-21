package cmdutil

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/zetic-ai/melange-cli/internal/api"
)

// NewAPIClient builds an api.Client for the given host and token, honoring
// the factory's base transport override and MELANGE_DEBUG=1 (debug lines go
// to stderr). token may be empty for unauthenticated clients.
func NewAPIClient(f *Factory, host, token string) (*api.Client, error) {
	var debug io.Writer
	if os.Getenv("MELANGE_DEBUG") == "1" {
		debug = f.IOStreams.ErrOut
	}
	return api.NewClient(api.Options{
		Host:      host,
		Token:     token,
		UserAgent: fmt.Sprintf("melange-cli/%s (%s; %s)", f.Version, runtime.GOOS, runtime.GOARCH),
		Debug:     debug,
		Transport: f.HTTPTransport,
	})
}
