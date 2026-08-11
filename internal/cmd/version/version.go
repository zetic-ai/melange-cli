// Package version implements the `melange version` command.
package version

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/build"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// NewCmdVersion returns the `melange version` command.
func NewCmdVersion(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the " + f.Edition.ProgramName() + " CLI version",
		Args:  cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(f.IOStreams.Out, "%s version %s\n", f.Edition.ProgramName(), build.Info())
			return nil
		},
	}
}
