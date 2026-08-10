// Package authn resolves and refreshes credentials shared by CLI entry points.
package authn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
	"github.com/zetic-ai/melange-cli/internal/oauth"
)

// ErrSessionExpired indicates that the authorization server rejected the
// refresh grant and the stale OAuth credentials were cleared.
var ErrSessionExpired = errors.New("session expired, run melange auth login")

var refreshMu sync.Map

// ResolveAnyToken resolves credentials and refreshes stale OAuth tokens. A
// refresh is persisted to the same backend it was loaded from so a successful
// refresh cannot leave a stale, higher-precedence credential in another store.
func ResolveAnyToken(ctx context.Context, cfg *config.Config, issuerHost, hostKey string, transport http.RoundTripper) (config.Resolved, *config.OAuthCredentials, error) {
	res, creds, err := cfg.ResolveAnyTokenWith(hostKey, keyring.Lookup, keyring.LookupOAuth)
	if err != nil {
		return configuredPATFallback(cfg, hostKey, err)
	}
	if res.Value != "" || creds == nil {
		return res, creds, nil
	}

	muValue, _ := refreshMu.LoadOrStore(hostKey, &sync.Mutex{})
	mu := muValue.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	current, source, err := cfg.ResolveOAuth(hostKey, keyring.LookupOAuth)
	if err != nil {
		return configuredPATFallback(cfg, hostKey, err)
	}
	if current == nil {
		return config.Resolved{}, nil, nil
	}
	if !current.Expiry.IsZero() && current.Expiry.After(time.Now().Add(30*time.Second)) {
		return config.Resolved{Value: current.AccessToken, Source: source}, current, nil
	}
	if transport == nil {
		transport = http.DefaultTransport
	}

	token, err := oauth.RefreshWithTransport(ctx, issuerHost, current.ClientID, current.RefreshToken, transport)
	if err != nil {
		var oauthErr *oauth.OAuthError
		if errors.As(err, &oauthErr) && oauthErr.Code == "invalid_grant" {
			return config.Resolved{}, nil, clearExpiredCredentials(cfg, hostKey)
		}
		return config.Resolved{}, nil, err
	}
	if token.AccessToken == "" {
		return config.Resolved{}, nil, errors.New("oauth refresh response missing access_token")
	}

	updated := mergeRefresh(current, token)
	if err := persistRefresh(cfg, hostKey, source, updated); err != nil {
		return config.Resolved{}, nil, err
	}
	return config.Resolved{Value: updated.AccessToken, Source: source}, &updated, nil
}

func configuredPATFallback(cfg *config.Config, hostKey string, original error) (config.Resolved, *config.OAuthCredentials, error) {
	if cfg != nil && cfg.Hosts != nil {
		entry, ok := cfg.Hosts[hostKey]
		if ok && entry.Storage == config.CredentialStorageConfig && entry.APIKey != "" {
			res, err := cfg.ResolveTokenWith(hostKey, keyring.Lookup)
			if err != nil {
				return config.Resolved{}, nil, err
			}
			return res, nil, nil
		}
	}
	return config.Resolved{}, nil, original
}

func mergeRefresh(previous *config.OAuthCredentials, token *oauth.TokenResponse) config.OAuthCredentials {
	refreshToken := token.RefreshToken
	if refreshToken == "" {
		refreshToken = previous.RefreshToken
	}
	scope := token.Scope
	if scope == "" {
		scope = previous.Scope
	}
	tokenType := token.TokenType
	if tokenType == "" {
		tokenType = previous.TokenType
	}
	return config.OAuthCredentials{
		AccessToken:  token.AccessToken,
		RefreshToken: refreshToken,
		Expiry:       time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Add(-30 * time.Second),
		ClientID:     previous.ClientID,
		Scope:        scope,
		TokenType:    tokenType,
	}
}

func persistRefresh(cfg *config.Config, hostKey, source string, creds config.OAuthCredentials) error {
	switch source {
	case "oauth(keyring)":
		if err := keyring.Delete(hostKey); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("clearing PAT from keyring before OAuth refresh: %w", err)
		}
		if err := keyring.SetOAuth(hostKey, creds); err != nil {
			return fmt.Errorf("persisting refreshed OAuth credentials to keyring: %w", err)
		}
		return nil
	case "oauth(config)":
		var previous config.HostEntry
		var hadPrevious bool
		if cfg.Hosts != nil {
			previous, hadPrevious = cfg.Hosts[hostKey]
		}
		if err := cfg.SetHostOAuth(hostKey, creds); err != nil {
			if hadPrevious {
				cfg.Hosts[hostKey] = previous
			} else {
				delete(cfg.Hosts, hostKey)
			}
			return fmt.Errorf("persisting refreshed OAuth credentials to config: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown OAuth credential source %q", source)
	}
}

func clearExpiredCredentials(cfg *config.Config, hostKey string) error {
	var cleanup []error
	if err := keyring.DeleteOAuth(hostKey); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		cleanup = append(cleanup, err)
	}
	if err := cfg.DeleteHostOAuth(hostKey); err != nil {
		cleanup = append(cleanup, err)
	}
	if err := errors.Join(cleanup...); err != nil {
		return fmt.Errorf("%w; clearing stale credentials: %v", ErrSessionExpired, err)
	}
	return ErrSessionExpired
}
