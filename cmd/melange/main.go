// Command melange is the entry point for the melange CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/build"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/keyring"
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

	// ApiClient resolves host+token (env > keyring > config) and returns an
	// authenticated client. Only commands that require auth should call it:
	// it returns AuthError (exit 4) when no token is available.
	f.ApiClient = func() (*api.Client, error) {
		cfg, err := f.Config()
		if err != nil {
			return nil, err
		}
		host := cfg.ResolveHost(f.HostOverride)
		hostKey := keyring.HostKey(host.Value)
		token, err := cfg.ResolveTokenWith(hostKey, keyring.Lookup)
		if err != nil {
			return nil, err
		}
		if token.Value == "" {
			return nil, cmdutil.AuthError{Err: fmt.Errorf(
				"not logged in to %s; run `melange auth login` or set MELANGE_API_KEY", hostKey)}
		}
		return cmdutil.NewAPIClient(f, host.Value, token.Value)
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
		// errors.Is (not ==): typed errors may wrap ErrSilent to combine
		// "already printed" with a specific exit code (e.g. exit 4 from
		// `melange api` after it printed its own HTTP 401 summary).
		if !errors.Is(mappedErr, cmdutil.ErrSilent) {
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
