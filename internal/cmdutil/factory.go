package cmdutil

import (
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

// Factory carries the shared dependencies that every command receives.
// HTTP client slots are added in M1 — do not add them here.
type Factory struct {
	IOStreams  *iostreams.IOStreams
	Config     func() (*config.Config, error)
	Executable string
	Version    string
	NoInput    bool
}
