package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
	"github.com/zetic-ai/melange-cli/internal/oauth"
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

var oauthRefreshMu sync.Map

func (h *hostContext) resolveAnyToken(ctx context.Context) (config.Resolved, *config.OAuthCredentials, error) {
	res, creds, err := h.cfg.ResolveAnyTokenWith(h.hostKey, keyring.Lookup, keyring.LookupOAuth)
	if err != nil {
		// If OAuth keyring is locked but PAT config exists with explicit storage, don't hard fail
		if h.cfg.Hosts != nil {
			if entry, ok := h.cfg.Hosts[h.hostKey]; ok && entry.Storage == config.CredentialStorageConfig && entry.APIKey != "" {
				// Fall through to PAT via direct lookup
				res2, err2 := h.cfg.ResolveTokenWith(h.hostKey, keyring.Lookup)
				if err2 != nil {
					return config.Resolved{}, nil, err2
				}
				if res2.Value != "" {
					return res2, nil, nil
				}
				return config.Resolved{}, nil, nil
			}
		}
		return config.Resolved{}, nil, err
	}
	if res.Value != "" {
		return res, creds, nil
	}
	if creds != nil {
		muIface, _ := oauthRefreshMu.LoadOrStore(h.hostKey, &sync.Mutex{})
		mu := muIface.(*sync.Mutex)
		mu.Lock()
		defer mu.Unlock()
		creds2, src2, err := h.cfg.ResolveOAuth(h.hostKey, keyring.LookupOAuth)
		if err != nil {
			if h.cfg.Hosts != nil {
				if entry, ok := h.cfg.Hosts[h.hostKey]; ok && entry.Storage == config.CredentialStorageConfig && entry.APIKey != "" {
					// ignore oauth error, fall through to PAT
				} else {
					return config.Resolved{}, nil, err
				}
			} else {
				return config.Resolved{}, nil, err
			}
		} else if creds2 != nil && !creds2.Expiry.IsZero() && creds2.Expiry.After(time.Now().Add(30*time.Second)) {
			return config.Resolved{Value: creds2.AccessToken, Source: src2}, creds2, nil
		}
		if creds2 != nil {
			creds = creds2
			src := src2
			tr := h.transport
			if tr == nil {
				tr = http.DefaultTransport
			}
			newTok, err := oauth.RefreshWithTransport(ctx, h.host.Value, creds.ClientID, creds.RefreshToken, tr)
			if err != nil {
				var oe *oauth.OAuthError
				if errors.As(err, &oe) && oe.Code == "invalid_grant" {
					_ = keyring.DeleteOAuth(h.hostKey)
					_ = h.cfg.DeleteHostOAuth(h.hostKey)
					return config.Resolved{}, nil, cmdutil.AuthError{Err: fmt.Errorf("session expired, run melange auth login")}
				}
				if strings.Contains(err.Error(), "invalid_grant") {
					_ = keyring.DeleteOAuth(h.hostKey)
					_ = h.cfg.DeleteHostOAuth(h.hostKey)
					return config.Resolved{}, nil, cmdutil.AuthError{Err: fmt.Errorf("session expired, run melange auth login")}
				}
				return config.Resolved{}, nil, err
			}
			expiry := time.Now().Add(time.Duration(newTok.ExpiresIn) * time.Second).Add(-30 * time.Second)
			newCreds := config.OAuthCredentials{
				AccessToken:  newTok.AccessToken,
				RefreshToken: newTok.RefreshToken,
				Expiry:       expiry,
				ClientID:     creds.ClientID,
				Scope:        newTok.Scope,
				TokenType:    newTok.TokenType,
			}
			if kerr := keyring.SetOAuth(h.hostKey, newCreds); kerr == nil {
				if h.cfg.Hosts != nil {
					if entry, ok := h.cfg.Hosts[h.hostKey]; ok && entry.Storage == config.CredentialStorageConfig {
						delete(h.cfg.Hosts, h.hostKey)
						_ = config.Save(h.cfg)
					}
				}
				_ = keyring.Delete(h.hostKey)
			} else {
				if h.cfg.Hosts != nil {
					if entry, ok := h.cfg.Hosts[h.hostKey]; ok && entry.Storage == config.CredentialStorageConfig {
						_ = h.cfg.SetHostOAuth(h.hostKey, newCreds)
					}
				}
			}
			if src == "" {
				src = "oauth(keyring)"
			}
			return config.Resolved{Value: newCreds.AccessToken, Source: src}, &newCreds, nil
		}
	}
	// Already handled PAT fallback; if we reached here, no token
	return res, nil, nil
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
