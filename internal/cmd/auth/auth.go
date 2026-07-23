// Package auth implements the `melange auth` command family: login, logout,
// status, and token.
package auth

import (
	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// NewCmdAuth returns the `melange auth` parent command.
func NewCmdAuth(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth <command>",
		Short: "Authenticate melange with the Melange platform",
		Args:  cmdutil.CommandGroupArgs,
		RunE:  cmdutil.ShowCommandGroupHelp,
		Long: `Manage authentication for the Melange API.

Credentials are personal access tokens (prefix ztp_) created at
Settings → Personal Access Tokens. Tokens are resolved in this order:
MELANGE_API_KEY > MELANGE_API_KEY_FILE > explicitly selected config storage >
OS keyring > legacy config fallback.`,
		Example: `  # Log in interactively (paste a token)
  melange auth login

  # Log in from a file, for scripts and agents
  melange auth login --with-token < token.txt

  # Check who you are, as JSON
  MELANGE_API_KEY=ztp_... melange auth status --json`,
	}

	cmd.AddCommand(newCmdLogin(f))
	cmd.AddCommand(newCmdStatus(f))
	cmd.AddCommand(newCmdToken(f))
	cmd.AddCommand(newCmdLogout(f))

	return cmd
}
