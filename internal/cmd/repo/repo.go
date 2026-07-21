// Package repo implements `melange repo` — commands for managing model
// repositories on the Melange platform.
package repo

import (
	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// NewCmdRepo builds the `melange repo` command group.
func NewCmdRepo(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo <command>",
		Short: "Manage model repositories",
		Long: `Work with Melange model repositories: list the repositories your token
can see, inspect a single repository, and create new ones.

Repositories are addressed as ACCOUNT/NAME. Where the account is omitted,
it resolves to the account behind your token (one extra /v1/me call).

Data is written to stdout; progress and messages go to stderr. All
subcommands support --json, --jq, and --template for structured output.`,
		Example: `  # List your repositories
  melange repo list

  # Inspect one repository
  melange repo view zetic/whisper-tiny

  # Create a private repository
  melange repo create whisper-tiny --private`,
	}

	cmd.AddCommand(newCmdList(f))
	cmd.AddCommand(newCmdView(f))
	cmd.AddCommand(newCmdCreate(f))

	return cmd
}
