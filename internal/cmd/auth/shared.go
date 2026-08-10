package auth

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/authn"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
)

// hostContext is the resolved host for the current invocation.
type hostContext struct {
	cfg       *config.Config
	host      config.Resolved
	hostKey   string
	transport http.RoundTripper
}

func resolveHost(f *cmdutil.Factory) (*hostContext, error) {
	cfg, err := f.Config()
	if err != nil {
		return nil, err
	}
	host := cfg.ResolveHost(f.HostOverride)
	return &hostContext{
		cfg:       cfg,
		host:      host,
		hostKey:   keyring.HostKey(host.Value),
		transport: f.HTTPTransport,
	}, nil
}

func (h *hostContext) resolveAnyToken(ctx context.Context) (config.Resolved, *config.OAuthCredentials, error) {
	res, creds, err := authn.ResolveAnyToken(ctx, h.cfg, h.host.Value, h.hostKey, h.transport)
	if errors.Is(err, authn.ErrSessionExpired) {
		return config.Resolved{}, nil, cmdutil.AuthError{Err: err}
	}
	return res, creds, err
}

func storageLocation(source string) string {
	switch source {
	case "keyring":
		return "keyring"
	case "config":
		return filepath.Join(config.ConfigDir(), "config.yml")
	case "oauth(keyring)":
		return "keyring"
	case "oauth(config)":
		return filepath.Join(config.ConfigDir(), "config.yml")
	default:
		if strings.HasPrefix(source, "oauth") {
			return "keyring"
		}
		if strings.HasPrefix(source, "env:") {
			return "environment (not stored)"
		}
		return "environment (not stored)"
	}
}

func scopeList(scopes []string) string {
	if len(scopes) == 0 {
		return "none"
	}
	return strings.Join(scopes, ", ")
}
