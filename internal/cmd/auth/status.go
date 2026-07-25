package auth

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func newCmdStatus(f *cmdutil.Factory) *cobra.Command {
	var exporter *cmdutil.Exporter

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show authentication status for the current host",
		Long: `Verify the resolved token against the API and report the identity
behind it: host, account, token name, scopes, where the token came from
(env, keyring, or config), and where it is stored. When the server supports
it, the account's billing plan is shown too (use "melange plan" for detail).

Exit codes: 0 authenticated, 1 network/API error, 4 not logged in or the
token was rejected.`,
		Example: `  # Human-readable status
  melange auth status

  # Agent pattern: verify a token non-interactively
  MELANGE_API_KEY=ztp_... melange auth status --json`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := resolveHost(f)
			if err != nil {
				return err
			}
			token, err := host.resolveToken()
			if err != nil {
				return err
			}
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

			// Best-effort: the plan enriches status but is not what status
			// verifies. A read failure (e.g. a backend that predates the
			// endpoint) must not turn a valid token into an error, so the
			// plan line is simply omitted when unavailable.
			var planName string
			if p, perr := client.GetBillingPlan(cmd.Context()); perr == nil {
				planName = string(p.Plan)
			}

			if exporter != nil {
				payload := map[string]any{
					"host":         host.hostKey,
					"account":      me.Account.Name,
					"scopes":       me.Token.Scopes,
					"token_name":   me.Token.Name,
					"token_source": token.Source,
					"storage":      storageLocation(token.Source),
				}
				if planName != "" {
					payload["plan"] = planName
				}
				return exporter.Write(f.IOStreams, payload)
			}
			out := f.IOStreams.Out
			fmt.Fprintf(out, "Host: %s\n", text.SanitizeTerminalInline(host.hostKey))
			fmt.Fprintf(out, "Account: %s (%s)\n",
				text.SanitizeTerminalInline(me.Account.Name),
				text.SanitizeTerminalInline(me.Account.Type))
			fmt.Fprintf(out, "User: %s\n", text.SanitizeTerminalInline(me.User.Email))
			fmt.Fprintf(out, "Token: %s\n", text.SanitizeTerminalInline(me.Token.Name))
			fmt.Fprintf(out, "Scopes: %s\n",
				text.SanitizeTerminalInline(scopeList(me.Token.Scopes)))
			if planName != "" {
				fmt.Fprintf(out, "Plan: %s\n", text.SanitizeTerminalInline(planName))
			}
			fmt.Fprintf(out, "Source: %s\n", text.SanitizeTerminalInline(token.Source))
			fmt.Fprintf(out, "Storage: %s\n",
				text.SanitizeTerminalInline(storageLocation(token.Source)))
			return nil
		},
	}

	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}
