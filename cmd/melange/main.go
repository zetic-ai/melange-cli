// Command melange is the entry point for the melange CLI.
package main

import (
	"context"
	"net/http"
	"os"

	"github.com/zetic-ai/melange-cli/internal/cliapp"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/edition"
)

func main() {
	code := Run(os.Args[1:])
	os.Exit(code)
}

// Run executes the CLI with the given args and returns the exit code.
// It is a separate function so tests can call it without triggering os.Exit.
func Run(args []string) int {
	return cliapp.Run(args, edition.Standard())
}

// mapCobraError inspects cobra error messages and promotes certain error
// classes to the appropriate typed error so ExitCode maps them correctly.
func mapCobraError(err error) error {
	return cliapp.MapCobraError(err)
}

func resolveAnyTokenMain(ctx context.Context, cfg *config.Config, issuerHost, hostKey string, transport http.RoundTripper) (config.Resolved, *config.OAuthCredentials, error) {
	return cliapp.ResolveAnyToken(ctx, cfg, issuerHost, hostKey, transport)
}
