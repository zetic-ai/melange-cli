// Command melange is the entry point for the melange CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/build"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/keyring"
	"github.com/zetic-ai/melange-cli/internal/oauth"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func main() {
	code := Run(os.Args[1:])
	os.Exit(code)
}

// Run executes the CLI with the given args and returns the exit code.
// It is a separate function so tests can call it without triggering os.Exit.
func Run(args []string) int {
	ios := iostreams.System()

	// Build factory.
	f := &cmdutil.Factory{
		IOStreams:  ios,
		Executable: executable(),
		Version:    build.Version,
		Config: func() (*config.Config, error) {
			return config.Load()
		},
	}

	// ApiClient resolves host+token (env > env file > explicitly selected
	// config > keyring > legacy config fallback) and returns an authenticated
	// client. Only commands that require auth should call it; it returns
	// AuthError (exit 4) when no token is available.
	f.ApiClient = func() (*api.Client, error) {
		cfg, err := f.Config()
		if err != nil {
			return nil, err
		}
		host := cfg.ResolveHost(f.HostOverride)
		hostKey := keyring.HostKey(host.Value)
		transport := f.HTTPTransport
		if transport == nil {
			transport = http.DefaultTransport
		}
		ctx := context.Background()
		token, _, err := resolveAnyTokenMain(ctx, cfg, host.Value, hostKey, transport)
		if err != nil {
			return nil, err
		}
		if token.Value == "" {
			return nil, cmdutil.AuthError{Err: fmt.Errorf("not logged in to %s; run `melange auth login` or set MELANGE_API_KEY", hostKey)}
		}
		return cmdutil.NewAPIClient(f, host.Value, token.Value)
	}

	// Build root command.
	rootCmd := root.NewCmdRoot(f)
	rootCmd.SetArgs(args)

	// Handle SIGINT: cancel the context so commands can react gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		// Cobra surfaces unknown command errors as plain strings;
		// map them to FlagError (exit 2) so the contract is met.
		mappedErr := mapCobraError(err)
		code := cmdutil.ExitCode(mappedErr)
		// errors.Is (not ==): typed errors may wrap ErrSilent to combine
		// "already printed" with a specific exit code (e.g. exit 4 from
		// `melange api` after it printed its own HTTP 401 summary).
		if !errors.Is(mappedErr, cmdutil.ErrSilent) {
			fmt.Fprintf(ios.ErrOut, "melange: %s\n", text.SanitizeTerminal(mappedErr.Error()))
		}
		return code
	}
	return 0
}

// mapCobraError inspects cobra error messages and promotes certain error
// classes to the appropriate typed error so ExitCode maps them correctly.
func mapCobraError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown command") || strings.Contains(msg, "unknown flag") || strings.Contains(msg, "invalid flag") {
		return cmdutil.FlagError{Err: err}
	}
	if strings.Contains(msg, "required flag") || strings.Contains(msg, "flag needs") {
		return cmdutil.FlagError{Err: err}
	}
	if strings.HasPrefix(msg, "unknown command") {
		return cmdutil.FlagError{Err: err}
	}
	return err
}

// executable returns the path of the running binary, or "melange" on failure.
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return "melange"
	}
	return exe
}

func resolveAnyTokenMain(ctx context.Context, cfg *config.Config, issuerHost, hostKey string, transport http.RoundTripper) (config.Resolved, *config.OAuthCredentials, error) {
	res, creds, err := cfg.ResolveAnyTokenWith(hostKey, keyring.Lookup, keyring.LookupOAuth)
	if err != nil {
		if cfg.Hosts != nil {
			if entry, ok := cfg.Hosts[hostKey]; ok && entry.Storage == config.CredentialStorageConfig && entry.APIKey != "" {
				res2, err2 := cfg.ResolveTokenWith(hostKey, keyring.Lookup)
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
		muIface, _ := oauth.RefreshMu.LoadOrStore(hostKey, &sync.Mutex{})
		mu := muIface.(*sync.Mutex)
		mu.Lock()
		defer mu.Unlock()
		creds2, src2, err := cfg.ResolveOAuth(hostKey, keyring.LookupOAuth)
		if err != nil {
			if cfg.Hosts != nil {
				if entry, ok := cfg.Hosts[hostKey]; ok && entry.Storage == config.CredentialStorageConfig && entry.APIKey != "" {
					creds2 = nil
				} else {
					return config.Resolved{}, nil, err
				}
			} else {
				return config.Resolved{}, nil, err
			}
		}
		if creds2 != nil && !creds2.Expiry.IsZero() && creds2.Expiry.After(time.Now().Add(30*time.Second)) {
			return config.Resolved{Value: creds2.AccessToken, Source: src2}, creds2, nil
		}
		if creds2 != nil {
			creds = creds2
			src := src2
			if transport == nil {
				transport = http.DefaultTransport
			}
			newTok, err := oauth.RefreshWithTransport(ctx, issuerHost, creds.ClientID, creds.RefreshToken, transport)
			if err != nil {
				var oe *oauth.OAuthError
				if errors.As(err, &oe) && oe.Code == "invalid_grant" {
					_ = keyring.DeleteOAuth(hostKey)
					_ = cfg.DeleteHostOAuth(hostKey)
					return config.Resolved{}, nil, cmdutil.AuthError{Err: fmt.Errorf("session expired, run melange auth login")}
				}
				if strings.Contains(err.Error(), "invalid_grant") {
					_ = keyring.DeleteOAuth(hostKey)
					_ = cfg.DeleteHostOAuth(hostKey)
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
			if kerr := keyring.SetOAuth(hostKey, newCreds); kerr == nil {
				if cfg.Hosts != nil {
					if entry, ok := cfg.Hosts[hostKey]; ok && entry.Storage == config.CredentialStorageConfig {
						delete(cfg.Hosts, hostKey)
						_ = config.Save(cfg)
					}
				}
				_ = keyring.Delete(hostKey)
			} else {
				if cfg.Hosts != nil {
					if entry, ok := cfg.Hosts[hostKey]; ok && entry.Storage == config.CredentialStorageConfig {
						_ = cfg.SetHostOAuth(hostKey, newCreds)
					}
				}
			}
			if src == "" {
				src = "oauth(keyring)"
			}
			return config.Resolved{Value: newCreds.AccessToken, Source: src}, &newCreds, nil
		}
	}
	return res, nil, nil
}
