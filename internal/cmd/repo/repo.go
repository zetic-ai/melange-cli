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
		Use:     "repo <command>",
		Aliases: []string{"project"},
		Short:   "Manage model repositories",
		Args:    cmdutil.CommandGroupArgs,
		RunE:    cmdutil.ShowCommandGroupHelp,
		Long: `Work with Melange model repositories: list the repositories your token
can see, inspect a single repository, create new ones, edit their
metadata, and delete them.

Repositories are addressed as ACCOUNT/NAME. Where the account is omitted,
it resolves to the account behind your token (one extra /v1/me call);
destructive commands always require the full ACCOUNT/NAME.

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
	cmd.AddCommand(newCmdEdit(f))
	cmd.AddCommand(newCmdDelete(f))

	return cmd
}
