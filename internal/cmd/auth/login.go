package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
	"github.com/zetic-ai/melange-cli/internal/oauth"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func newCmdLogin(f *cmdutil.Factory) *cobra.Command {
	var (
		withToken       bool
		noBrowser       bool
		insecureStorage bool
		exporter        *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to the Melange platform",
		Long: `Authenticate with the Melange platform.

By default opens a browser for OAuth (recommended); use --with-token for personal access tokens (CI/headless).

The token is verified against the API, then stored in the OS keyring.
If the keyring is unavailable, pass --insecure-storage to store it in the
config file (created with 0600 permissions) instead.

Interactive token input is hidden. Non-interactive runs must use --with-token
or set MELANGE_API_KEY.

Exit codes: 0 success, 1 storage or validation error, 2 usage error
(non-interactive without --with-token), 4 token rejected by the API.`,
		Example: `  # Browser login (recommended)
  melange auth login

  # Headless / SSH without browser
  melange auth login --no-browser

  # Scripted login for agents and CI
  melange auth login --with-token < token.txt

  # Machine-readable result
  melange auth login --with-token --json < token.txt`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if withToken {
				return runPATLogin(cmd, f, insecureStorage, exporter)
			}
			if !f.IOStreams.IsStdinTTY() || f.NoInput {
				return cmdutil.FlagError{Err: errors.New("cannot prompt for a token in non-interactive mode; run `melange auth login --with-token < token.txt` or set MELANGE_API_KEY")}
			}
			host, err := resolveHost(f)
			if err != nil {
				return err
			}
			transport := f.HTTPTransport
			if transport == nil {
				transport = http.DefaultTransport
			}
			var creds *config.OAuthCredentials
			var oauthErr error
			if noBrowser {
				creds, oauthErr = oauth.LoginFlowWithOptionsWithTransport(cmd.Context(), host.host.Value, f.IOStreams.ErrOut, true, transport)
			} else {
				creds, oauthErr = oauth.LoginFlowWithTransport(cmd.Context(), host.host.Value, f.IOStreams.ErrOut, transport)
			}
			if oauthErr != nil {
				var oe *oauth.OAuthError
				if errors.As(oauthErr, &oe) {
					return cmdutil.AuthError{Err: fmt.Errorf("oauth: %w", oauthErr)}
				}
				fmt.Fprintf(f.IOStreams.ErrOut, "! Browser login unavailable (%s), falling back to personal access token.\n", text.SanitizeTerminalInline(oauthErr.Error()))
				if strings.Contains(oauthErr.Error(), "port") && strings.Contains(oauthErr.Error(), "ssh -L") {
					fmt.Fprintln(f.IOStreams.ErrOut, oauthErr.Error())
				} else if !noBrowser {
					if strings.Contains(oauthErr.Error(), "timeout") {
						fmt.Fprintln(f.IOStreams.ErrOut, oauthErr.Error())
					}
				}
				if noBrowser || strings.Contains(oauthErr.Error(), "timeout") || strings.Contains(oauthErr.Error(), "port") {
					fmt.Fprintf(f.IOStreams.ErrOut, "If using SSH, forward the callback port: ssh -L {port}:127.0.0.1:{port} user@host\n")
				}
				return runPATPromptLogin(cmd, f, host, insecureStorage, exporter)
			}
			storage, err := storeOAuth(host, *creds, insecureStorage)
			if err != nil {
				return err
			}
			client, err := cmdutil.NewAPIClient(f, host.host.Value, creds.AccessToken)
			if err != nil {
				return err
			}
			me, err := client.GetMe(cmd.Context())
			if err != nil {
				var apiErr *api.Error
				if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
					return cmdutil.AuthError{Err: fmt.Errorf("%s rejected the token (%s); create a new one at Settings → Personal Access Tokens", host.hostKey, apiErr.Message)}
				}
				return err
			}
			for _, env := range []string{config.EnvAPIKey, config.EnvAPIKeyFile} {
				if os.Getenv(env) != "" {
					fmt.Fprintf(f.IOStreams.ErrOut, "! %s is set and takes precedence over stored credentials\n", env)
				}
			}
			if exporter != nil {
				return exporter.Write(f.IOStreams, map[string]any{
					"host":      host.hostKey,
					"account":   me.Account.Name,
					"scopes":    me.Token.Scopes,
					"storage":   storage,
					"auth_type": "oauth",
				})
			}
			fmt.Fprintf(f.IOStreams.ErrOut, "✓ Logged in to %s as %s (scopes: %s, storage: %s, via browser)\n",
				text.SanitizeTerminalInline(host.hostKey),
				text.SanitizeTerminalInline(me.Account.Name),
				text.SanitizeTerminalInline(scopeList(me.Token.Scopes)),
				text.SanitizeTerminalInline(storage))
			return nil
		},
	}

	cmd.Flags().BoolVar(&withToken, "with-token", false, "Read a personal access token (ztp_) from standard input (CI/headless)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the authorize URL and wait for callback without opening a browser (SSH/headless)")
	cmd.Flags().BoolVar(&insecureStorage, "insecure-storage", false, "Store the token in the config file when the OS keyring is unavailable")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

func runPATLogin(cmd *cobra.Command, f *cmdutil.Factory, insecureStorage bool, exporter *cmdutil.Exporter) error {
	token, err := readToken(cmd.Context(), f, true)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(token, "ztp_") {
		return errors.New("not a Melange personal access token (expected ztp_ prefix)")
	}
	host, err := resolveHost(f)
	if err != nil {
		return err
	}
	client, err := cmdutil.NewAPIClient(f, host.host.Value, token)
	if err != nil {
		return err
	}
	me, err := client.GetMe(cmd.Context())
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
			return cmdutil.AuthError{Err: fmt.Errorf("%s rejected the token (%s); create a new one at Settings → Personal Access Tokens", host.hostKey, apiErr.Message)}
		}
		return err
	}
	storage, err := storeToken(host, token, insecureStorage)
	if err != nil {
		return err
	}
	for _, env := range []string{config.EnvAPIKey, config.EnvAPIKeyFile} {
		if os.Getenv(env) != "" {
			fmt.Fprintf(f.IOStreams.ErrOut, "! %s is set and takes precedence over stored credentials\n", env)
		}
	}
	if exporter != nil {
		return exporter.Write(f.IOStreams, map[string]any{
			"host":      host.hostKey,
			"account":   me.Account.Name,
			"scopes":    me.Token.Scopes,
			"storage":   storage,
			"auth_type": "pat",
		})
	}
	fmt.Fprintf(f.IOStreams.ErrOut, "✓ Logged in to %s as %s (token: %s, scopes: %s, storage: %s)\n",
		text.SanitizeTerminalInline(host.hostKey),
		text.SanitizeTerminalInline(me.Account.Name),
		text.SanitizeTerminalInline(me.Token.Name),
		text.SanitizeTerminalInline(scopeList(me.Token.Scopes)),
		text.SanitizeTerminalInline(storage))
	return nil
}

func runPATPromptLogin(cmd *cobra.Command, f *cmdutil.Factory, host *hostContext, insecureStorage bool, exporter *cmdutil.Exporter) error {
	token, err := readToken(cmd.Context(), f, false)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(token, "ztp_") {
		return errors.New("not a Melange personal access token (expected ztp_ prefix)")
	}
	client, err := cmdutil.NewAPIClient(f, host.host.Value, token)
	if err != nil {
		return err
	}
	me, err := client.GetMe(cmd.Context())
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
			return cmdutil.AuthError{Err: fmt.Errorf("%s rejected the token (%s); create a new one at Settings → Personal Access Tokens", host.hostKey, apiErr.Message)}
		}
		return err
	}
	storage, err := storeToken(host, token, insecureStorage)
	if err != nil {
		return err
	}
	for _, env := range []string{config.EnvAPIKey, config.EnvAPIKeyFile} {
		if os.Getenv(env) != "" {
			fmt.Fprintf(f.IOStreams.ErrOut, "! %s is set and takes precedence over stored credentials\n", env)
		}
	}
	if exporter != nil {
		return exporter.Write(f.IOStreams, map[string]any{
			"host":      host.hostKey,
			"account":   me.Account.Name,
			"scopes":    me.Token.Scopes,
			"storage":   storage,
			"auth_type": "pat",
		})
	}
	fmt.Fprintf(f.IOStreams.ErrOut, "✓ Logged in to %s as %s (token: %s, scopes: %s, storage: %s)\n",
		text.SanitizeTerminalInline(host.hostKey),
		text.SanitizeTerminalInline(me.Account.Name),
		text.SanitizeTerminalInline(me.Token.Name),
		text.SanitizeTerminalInline(scopeList(me.Token.Scopes)),
		text.SanitizeTerminalInline(storage))
	return nil
}

func readToken(ctx context.Context, f *cmdutil.Factory, withToken bool) (string, error) {
	ios := f.IOStreams
	if withToken {
		raw, err := io.ReadAll(ios.In)
		if err != nil {
			return "", fmt.Errorf("reading token from stdin: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}

	if !ios.IsStdinTTY() || f.NoInput {
		return "", cmdutil.FlagError{Err: errors.New("cannot prompt for a token in non-interactive mode; run `melange auth login --with-token < token.txt` or set MELANGE_API_KEY")}
	}

	fmt.Fprint(ios.ErrOut, "Paste your personal access token (create one at Settings → Personal Access Tokens): ")
	raw, err := ios.ReadPassword(ctx)
	fmt.Fprintln(ios.ErrOut)
	if err != nil {
		return "", fmt.Errorf("reading token: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func storeToken(host *hostContext, token string, insecureStorage bool) (string, error) {
	kerr := keyring.Set(host.hostKey, token)
	if kerr == nil {
		if host.cfg.Hosts != nil {
			if entry, ok := host.cfg.Hosts[host.hostKey]; ok && entry.Storage == config.CredentialStorageConfig {
				delete(host.cfg.Hosts, host.hostKey)
				if err := config.Save(host.cfg); err != nil {
					_ = keyring.Delete(host.hostKey)
					return "", fmt.Errorf("stored the token in the OS keyring but could not clear the prior config credential: %w", err)
				}
			}
		}
		_ = keyring.DeleteOAuth(host.hostKey)
		_ = host.cfg.DeleteHostOAuth(host.hostKey)
		return "keyring", nil
	}
	if !insecureStorage {
		return "", fmt.Errorf("could not store the token in the OS keyring: %w\nRe-run with --insecure-storage to save it to the config file (0600), or skip storage entirely by setting MELANGE_API_KEY", kerr)
	}
	if err := host.cfg.SetHostAPIKey(host.hostKey, token); err != nil {
		return "", err
	}
	return "config", nil
}

func storeOAuth(host *hostContext, creds config.OAuthCredentials, insecureStorage bool) (string, error) {
	kerr := keyring.SetOAuth(host.hostKey, creds)
	if kerr == nil {
		if host.cfg.Hosts != nil {
			if entry, ok := host.cfg.Hosts[host.hostKey]; ok && entry.Storage == config.CredentialStorageConfig {
				delete(host.cfg.Hosts, host.hostKey)
				if err := config.Save(host.cfg); err != nil {
					_ = keyring.DeleteOAuth(host.hostKey)
					return "", fmt.Errorf("stored oauth in keyring but could not clear prior config credential: %w", err)
				}
			}
		}
		_ = keyring.Delete(host.hostKey)
		return "keyring", nil
	}
	if !insecureStorage {
		return "", fmt.Errorf("could not store oauth in keyring: %w\nRe-run with --insecure-storage to save it to the config file (0600), or skip storage entirely by setting MELANGE_API_KEY", kerr)
	}
	if err := host.cfg.SetHostOAuth(host.hostKey, creds); err != nil {
		return "", err
	}
	return "config", nil
}
