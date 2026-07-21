package repo

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

func newCmdDelete(f *cmdutil.Factory) *cobra.Command {
	var confirm string

	cmd := &cobra.Command{
		Use:   "delete <account/name>",
		Short: "Delete a repository",
		Long: `Delete a repository and everything in it. This cannot be undone.

The full ACCOUNT/NAME is always required — a bare name is never resolved
against your own account for destructive commands.

On a terminal you are asked to type the full ACCOUNT/NAME to confirm.
Non-interactively (or with --no-input) the prompt is replaced by
--confirm ACCOUNT/NAME, which must match the argument exactly.

Deleting a repository is restricted to the repository owner server-side.

Exit codes: 0 deleted, 1 API error or rejected confirmation, 2 usage
error (including missing/mismatched --confirm), 4 not authenticated.`,
		Example: `  # Delete interactively (type the full name at the prompt)
  melange repo delete zetic/whisper-tiny

  # Agent pattern: delete without a prompt
  melange repo delete zetic/whisper-tiny --confirm zetic/whisper-tiny

  # Verify it is gone (exits 1 once deleted)
  melange repo view zetic/whisper-tiny --json`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			account, name, err := splitRepoArg(args[0])
			if err != nil {
				return err
			}
			if account == "" {
				return cmdutil.FlagError{Err: fmt.Errorf(
					"deleting requires the full ACCOUNT/NAME (got %q); destructive commands never resolve a default account", args[0])}
			}
			full := account + "/" + name

			if err := confirmDeletion(f, full, confirm); err != nil {
				return err
			}

			g, _, err := genClient(f)
			if err != nil {
				return err
			}
			resp, err := g.DeleteRepoWithResponse(cmd.Context(), account, name)
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}

			fmt.Fprintf(f.IOStreams.ErrOut, "✓ Deleted repository %s\n", full)
			return nil
		},
	}

	cmd.Flags().StringVar(&confirm, "confirm", "",
		"Confirm deletion by repeating the full `ACCOUNT/NAME` (required when not a terminal)")

	return cmd
}

// confirmDeletion gates the destructive call: an exact --confirm value, or an
// interactive typed-name prompt. Anything else never reaches the API.
func confirmDeletion(f *cmdutil.Factory, full, confirm string) error {
	if confirm != "" {
		if confirm != full {
			return cmdutil.FlagError{Err: fmt.Errorf(
				"--confirm %q does not match %q; repeat the full ACCOUNT/NAME exactly", confirm, full)}
		}
		return nil
	}

	ios := f.IOStreams
	if !ios.IsStdinTTY() || f.NoInput {
		return cmdutil.FlagError{Err: fmt.Errorf(
			"deleting %s requires confirmation; re-run with --confirm %s", full, full)}
	}

	fmt.Fprintf(ios.ErrOut, "Deleting %s cannot be undone.\nType %s to confirm: ", full, full)
	line, err := bufio.NewReader(ios.In).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return fmt.Errorf("reading confirmation: %w", err)
	}
	if strings.TrimSpace(line) != full {
		return fmt.Errorf("confirmation did not match %s; repository not deleted", full)
	}
	return nil
}
