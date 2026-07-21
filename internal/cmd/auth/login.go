package auth

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
)

func newCmdLogin(f *cmdutil.Factory) *cobra.Command {
	var (
		withToken       bool
		insecureStorage bool
		jsonOutput      bool
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to the Melange platform with a personal access token",
		Long: `Authenticate with a Melange personal access token (prefix ztp_).

The token is verified against the API, then stored in the OS keyring.
If the keyring is unavailable, pass --insecure-storage to store it in the
config file (created with 0600 permissions) instead.

Interactively, the token is read from a paste prompt (input is not hidden —
paste, don't type). Non-interactive runs must use --with-token or set
MELANGE_API_KEY.

Exit codes: 0 success, 1 storage or validation error, 2 usage error
(non-interactive without --with-token), 4 token rejected by the API.`,
		Example: `  # Interactive login (paste the token at the prompt)
  melange auth login

  # Scripted login for agents and CI
  melange auth login --with-token < token.txt

  # Machine-readable result
  melange auth login --with-token --json < token.txt`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := readToken(f, withToken)
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
					return cmdutil.AuthError{Err: fmt.Errorf(
						"%s rejected the token (%s); create a new one at Settings → Personal Access Tokens",
						host.hostKey, apiErr.Message)}
				}
				return err
			}

			if os.Getenv(config.EnvAPIKey) != "" {
				fmt.Fprintf(f.IOStreams.ErrOut,
					"! %s is set and takes precedence over stored credentials\n", config.EnvAPIKey)
			}

			storage, err := storeToken(host, token, insecureStorage)
			if err != nil {
				return err
			}

			if jsonOutput {
				return json.NewEncoder(f.IOStreams.Out).Encode(map[string]any{
					"host":    host.hostKey,
					"account": me.Account.Name,
					"scopes":  me.Token.Scopes,
					"storage": storage,
				})
			}
			fmt.Fprintf(f.IOStreams.ErrOut,
				"✓ Logged in to %s as %s (token: %s, scopes: %s, storage: %s)\n",
				host.hostKey, me.Account.Name, me.Token.Name, scopeList(me.Token.Scopes), storage)
			return nil
		},
	}

	cmd.Flags().BoolVar(&withToken, "with-token", false, "Read the token from standard input")
	cmd.Flags().BoolVar(&insecureStorage, "insecure-storage", false,
		"Store the token in the config file when the OS keyring is unavailable")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the result as JSON")

	return cmd
}

// readToken obtains the token from stdin (--with-token) or an interactive
// paste prompt. Non-interactive invocations without --with-token are a usage
// error (exit 2).
func readToken(f *cmdutil.Factory, withToken bool) (string, error) {
	ios := f.IOStreams
	if withToken {
		raw, err := io.ReadAll(ios.In)
		if err != nil {
			return "", fmt.Errorf("reading token from stdin: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}

	if !ios.IsStdinTTY() || f.NoInput {
		return "", cmdutil.FlagError{Err: errors.New(
			"cannot prompt for a token in non-interactive mode; " +
				"run `melange auth login --with-token < token.txt` or set MELANGE_API_KEY")}
	}

	fmt.Fprint(ios.ErrOut,
		"Paste your personal access token (create one at Settings → Personal Access Tokens): ")
	line, err := bufio.NewReader(ios.In).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("reading token: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// storeToken saves the token to the keyring, falling back to the config file
// only when --insecure-storage was given. It returns the storage used.
func storeToken(host *hostContext, token string, insecureStorage bool) (string, error) {
	kerr := keyring.Set(host.hostKey, token)
	if kerr == nil {
		return "keyring", nil
	}
	if !insecureStorage {
		return "", fmt.Errorf(
			"could not store the token in the OS keyring: %w\n"+
				"Re-run with --insecure-storage to save it to the config file (0600), "+
				"or skip storage entirely by setting MELANGE_API_KEY", kerr)
	}
	if err := host.cfg.SetHostAPIKey(host.hostKey, token); err != nil {
		return "", err
	}
	return "config", nil
}
