package auth

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

func newCmdToken(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print the resolved authentication token",
		Long: `Print the raw token that melange would use for the current host,
followed by a newline. Nothing else is written to stdout, so the output is
safe to pipe into other tools.

Exit codes: 0 token printed, 4 no token found.`,
		Example: `  # Reuse the stored token in a curl call
  curl -H "Authorization: Bearer $(melange auth token)" https://api.zetic.ai/v1/me

  # Export it for a child process
  export MELANGE_API_KEY="$(melange auth token)"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := resolveHost(f)
			if err != nil {
				return err
			}
			token := host.resolveToken()
			if token.Value == "" {
				return cmdutil.AuthError{Err: fmt.Errorf(
					"no token found for %s. Run `melange auth login` or set MELANGE_API_KEY",
					host.hostKey)}
			}
			fmt.Fprintf(f.IOStreams.Out, "%s\n", token.Value)
			return nil
		},
	}
}
