// Package cliapp owns the shared process runner used by Melange CLI editions.
package cliapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/authn"
	"github.com/zetic-ai/melange-cli/internal/build"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/edition"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/keyring"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// Run executes one CLI edition and returns its stable process exit code.
func Run(args []string, policy edition.Policy) int {
	ios := iostreams.System()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		Executable: executable(policy.ProgramName()),
		Version:    build.Version,
		Edition:    policy,
		Config: func() (*config.Config, error) {
			return config.Load()
		},
	}
	f.ApiClient = func() (*api.Client, error) {
		cfg, err := f.Config()
		if err != nil {
			return nil, err
		}
		host := cfg.ResolveHost(f.HostOverride)
		hostKey := keyring.HostKey(host.Value)
		transport := f.HTTPTransport
		if transport == nil {
			transport = http.DefaultTransport
		}
		token, _, err := ResolveAnyToken(context.Background(), cfg, host.Value, hostKey, transport)
		if err != nil {
			return nil, err
		}
		if token.Value == "" {
			return nil, cmdutil.AuthError{Err: fmt.Errorf(
				"not logged in to %s; run `%s auth login` or set MELANGE_API_KEY",
				hostKey, policy.ProgramName())}
		}
		return cmdutil.NewAPIClient(f, host.Value, token.Value)
	}

	rootCmd := root.NewCmdRoot(f)
	rootCmd.SetArgs(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		mappedErr := MapCobraError(err)
		code := cmdutil.ExitCode(mappedErr)
		if !errors.Is(mappedErr, cmdutil.ErrSilent) {
			fmt.Fprintf(ios.ErrOut, "%s: %s\n", policy.ProgramName(), text.SanitizeTerminal(mappedErr.Error()))
		}
		return code
	}
	return 0
}

// MapCobraError promotes Cobra's string-only usage errors into the stable
// Melange exit-code taxonomy.
func MapCobraError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown command") || strings.Contains(msg, "unknown flag") || strings.Contains(msg, "invalid flag") {
		return cmdutil.FlagError{Err: err}
	}
	if strings.Contains(msg, "required flag") || strings.Contains(msg, "flag needs") {
		return cmdutil.FlagError{Err: err}
	}
	if strings.HasPrefix(msg, "unknown command") {
		return cmdutil.FlagError{Err: err}
	}
	return err
}

// ResolveAnyToken resolves and refreshes OAuth credentials, translating an
// expired session into the CLI's authentication error class.
func ResolveAnyToken(ctx context.Context, cfg *config.Config, issuerHost, hostKey string, transport http.RoundTripper) (config.Resolved, *config.OAuthCredentials, error) {
	res, creds, err := authn.ResolveAnyToken(ctx, cfg, issuerHost, hostKey, transport)
	if errors.Is(err, authn.ErrSessionExpired) {
		return config.Resolved{}, nil, cmdutil.AuthError{Err: err}
	}
	return res, creds, err
}

func executable(fallback string) string {
	exe, err := os.Executable()
	if err != nil {
		return fallback
	}
	return exe
}
