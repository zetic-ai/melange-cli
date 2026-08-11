package cmdutil

import (
	"net/http"

	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/edition"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

// Factory carries the shared dependencies that every command receives.
type Factory struct {
	IOStreams  *iostreams.IOStreams
	Config     func() (*config.Config, error)
	Executable string
	Version    string
	Edition    edition.Policy
	NoInput    bool

	// HostOverride is the value of the persistent --host flag, set by the
	// root command after flag parsing.
	HostOverride string

	// ApiClient returns an authenticated API client for the resolved
	// host+token. It returns AuthError when no token is available, so only
	// commands that require auth should call it.
	ApiClient func() (*api.Client, error)

	// HTTPTransport overrides the base transport of API clients (tests
	// inject an httpmock.Registry here); nil means http.DefaultTransport.
	HTTPTransport http.RoundTripper
}
