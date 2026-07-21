package cmdutil

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Positional-arg validators mirroring cobra's, with errors wrapped in
// FlagError so arg-count mistakes exit 2 (usage error) like flag mistakes.
// Commands must use these instead of cobra's validators in Args fields.

// ExactArgs is cobra.ExactArgs with FlagError-wrapped errors.
func ExactArgs(n int) cobra.PositionalArgs {
	return wrapPositionalArgs(cobra.ExactArgs(n))
}

// MaximumNArgs is cobra.MaximumNArgs with FlagError-wrapped errors.
func MaximumNArgs(n int) cobra.PositionalArgs {
	return wrapPositionalArgs(cobra.MaximumNArgs(n))
}

// NoArgs is cobra.NoArgs with FlagError-wrapped errors.
func NoArgs(cmd *cobra.Command, args []string) error {
	return wrapPositionalArgs(cobra.NoArgs)(cmd, args)
}

// wrapPositionalArgs converts a validator's errors into FlagError, adding the
// same usage hint the root command's FlagErrorFunc adds to flag errors.
func wrapPositionalArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return FlagError{Err: fmt.Errorf(
				"%w\nRun '%s --help' for usage", err, cmd.CommandPath())}
		}
		return nil
	}
}
