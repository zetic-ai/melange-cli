package auth

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
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

			var deleteErrs []error
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
