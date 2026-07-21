package auth

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

func newCmdStatus(f *cmdutil.Factory) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show authentication status for the current host",
		Long: `Verify the resolved token against the API and report the identity
behind it: host, account, token name, scopes, where the token came from
(env, keyring, or config), and where it is stored.

Exit codes: 0 authenticated, 1 network/API error, 4 not logged in or the
token was rejected.`,
		Example: `  # Human-readable status
  melange auth status

  # Agent pattern: verify a token non-interactively
  MELANGE_API_KEY=ztp_... melange auth status --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := resolveHost(f)
			if err != nil {
				return err
			}
			token := host.resolveToken()
			if token.Value == "" {
				//nolint:staticcheck // user-facing sentence-style message
				return cmdutil.AuthError{Err: fmt.Errorf(
					"Not logged in to %s. Run `melange auth login` or set MELANGE_API_KEY",
					host.hostKey)}
			}

			client, err := cmdutil.NewAPIClient(f, host.host.Value, token.Value)
			if err != nil {
				return err
			}
			me, err := client.GetMe(cmd.Context())
			if err != nil {
				var apiErr *api.Error
				if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
					return cmdutil.AuthError{Err: fmt.Errorf(
						"token from %s was rejected by %s (%s); run `melange auth login` to replace it",
						token.Source, host.hostKey, apiErr.Message)}
				}
				return err
			}

			out := f.IOStreams.Out
			if jsonOutput {
				return json.NewEncoder(out).Encode(map[string]any{
					"host":         host.hostKey,
					"account":      me.Account.Name,
					"scopes":       me.Token.Scopes,
					"token_name":   me.Token.Name,
					"token_source": token.Source,
					"storage":      storageLocation(token.Source),
				})
			}
			fmt.Fprintf(out, "Host: %s\n", host.hostKey)
			fmt.Fprintf(out, "Account: %s (%s)\n", me.Account.Name, me.Account.Type)
			fmt.Fprintf(out, "User: %s\n", me.User.Email)
			fmt.Fprintf(out, "Token: %s\n", me.Token.Name)
			fmt.Fprintf(out, "Scopes: %s\n", scopeList(me.Token.Scopes))
			fmt.Fprintf(out, "Source: %s\n", token.Source)
			fmt.Fprintf(out, "Storage: %s\n", storageLocation(token.Source))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output status as JSON")

	return cmd
}
