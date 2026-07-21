// Command melange is the entry point for the melange CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/build"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

func main() {
	code := Run(os.Args[1:])
	os.Exit(code)
}

// Run executes the CLI with the given args and returns the exit code.
// It is a separate function so tests can call it without triggering os.Exit.
func Run(args []string) int {
	ios := iostreams.System()

	// Build factory.
	f := &cmdutil.Factory{
		IOStreams:  ios,
		Executable: executable(),
		Version:    build.Version,
		Config: func() (*config.Config, error) {
			return config.Load()
		},
	}

	// Build root command.
	rootCmd := root.NewCmdRoot(f)
	rootCmd.SetArgs(args)

	// Handle SIGINT: cancel the context so commands can react gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		// Cobra surfaces unknown command errors as plain strings;
		// map them to FlagError (exit 2) so the contract is met.
		mappedErr := mapCobraError(err)
		code := cmdutil.ExitCode(mappedErr)
		if mappedErr != cmdutil.ErrSilent {
			fmt.Fprintf(ios.ErrOut, "melange: %s\n", mappedErr.Error())
		}
		return code
	}
	return 0
}

// mapCobraError inspects cobra error messages and promotes certain error
// classes to the appropriate typed error so ExitCode maps them correctly.
func mapCobraError(err error) error {
	msg := err.Error()
	// Cobra's unknown-command message starts with "unknown command"
	if strings.HasPrefix(msg, "unknown command") {
		return cmdutil.FlagError{Err: err}
	}
	return err
}

// executable returns the path of the running binary, or "melange" on failure.
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return "melange"
	}
	return exe
}
