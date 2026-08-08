package auth

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
	"github.com/zetic-ai/melange-cli/internal/oauth"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func newCmdLogout(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials for the current host",
		Long: `Delete the token stored for the resolved host from both the OS keyring
and the config file. Environment variables are not touched: if
MELANGE_API_KEY or MELANGE_API_KEY_FILE is set, it still takes precedence
and a note is printed.

Exit codes: 0 success, 1 storage error.`,
		Example: `  # Log out of the default host
  melange auth logout

  # Log out of a specific host
  MELANGE_HOST=api.staging.zetic.ai melange auth logout`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := resolveHost(f)
			if err != nil {
				return err
			}

			// Revoke OAuth if present (best-effort, ignore errors)
			transport := host.transport
			if transport == nil {
				transport = http.DefaultTransport
			}
			if creds, _, _ := host.cfg.ResolveOAuth(host.hostKey, keyring.LookupOAuth); creds != nil {
				_ = oauth.RevokeWithTransport(cmd.Context(), host.host.Value, creds.ClientID, creds.RefreshToken, transport)
				_ = oauth.RevokeWithTransport(cmd.Context(), host.host.Value, creds.ClientID, creds.AccessToken, transport)
			} else if creds2, ok, _ := keyring.LookupOAuth(host.hostKey); ok && creds2 != nil {
				_ = oauth.RevokeWithTransport(cmd.Context(), host.host.Value, creds2.ClientID, creds2.RefreshToken, transport)
				_ = oauth.RevokeWithTransport(cmd.Context(), host.host.Value, creds2.ClientID, creds2.AccessToken, transport)
			}

			var deleteErrs []error
			if err := keyring.DeleteOAuth(host.hostKey); err != nil && !errors.Is(err, keyring.ErrNotFound) {
				deleteErrs = append(deleteErrs, err)
			}
			if err := host.cfg.DeleteHostOAuth(host.hostKey); err != nil {
				deleteErrs = append(deleteErrs, err)
			}
			if err := keyring.Delete(host.hostKey); err != nil && !errors.Is(err, keyring.ErrNotFound) {
				deleteErrs = append(deleteErrs, err)
			}
			if err := host.cfg.DeleteHostAPIKey(host.hostKey); err != nil {
				deleteErrs = append(deleteErrs, err)
			}
			if err := errors.Join(deleteErrs...); err != nil {
				return err
			}

			errOut := f.IOStreams.ErrOut
			for _, env := range []string{config.EnvAPIKey, config.EnvAPIKeyFile} {
				if os.Getenv(env) != "" {
					fmt.Fprintf(errOut, "! %s is still set and takes precedence over stored credentials\n", env)
				}
			}
			fmt.Fprintf(errOut, "✓ Logged out of %s\n",
				text.SanitizeTerminalInline(host.hostKey))
			return nil
		},
	}
}
