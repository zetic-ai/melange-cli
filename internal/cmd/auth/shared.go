package auth

import (
	"path/filepath"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
)

// hostContext is the resolved host for the current invocation.
type hostContext struct {
	cfg     *config.Config
	host    config.Resolved // full host value (may include scheme)
	hostKey string          // normalized key used for keyring/config storage and display
}

// resolveHost loads config and resolves the API host, honoring --host.
func resolveHost(f *cmdutil.Factory) (*hostContext, error) {
	cfg, err := f.Config()
	if err != nil {
		return nil, err
	}
	host := cfg.ResolveHost(f.HostOverride)
	return &hostContext{
		cfg:     cfg,
		host:    host,
		hostKey: keyring.HostKey(host.Value),
	}, nil
}

// resolveToken resolves the token for the host, including the OS keyring.
// A set-but-unreadable MELANGE_API_KEY_FILE surfaces as a hard error.
func (h *hostContext) resolveToken() (config.Resolved, error) {
	return h.cfg.ResolveTokenWith(h.hostKey, keyring.Lookup)
}

// storageLocation describes where a token with the given source lives.
func storageLocation(source string) string {
	switch source {
	case "keyring":
		return "keyring"
	case "config":
		return filepath.Join(config.ConfigDir(), "config.yml")
	default:
		return "environment (not stored)"
	}
}

// scopeList renders token scopes for humans.
func scopeList(scopes []string) string {
	if len(scopes) == 0 {
		return "none"
	}
	return strings.Join(scopes, ", ")
}
