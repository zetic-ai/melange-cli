// Package library implements `melange library` — browsing the public model
// library: listing and filtering models, viewing a single model with its
// readme, and listing the providers.
package library

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// NewCmdLibrary builds the `melange library` command group.
func NewCmdLibrary(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "library <command>",
		Short: "Browse the public model library",
		Args:  cmdutil.CommandGroupArgs,
		RunE:  cmdutil.ShowCommandGroupHelp,
		Long: `Browse the public Melange model library: list and filter models,
inspect a single model (with its readme), and list the providers.

Data is written to stdout; progress and messages go to stderr. All
subcommands support --json, --jq, and --template for structured output.`,
		Example: `  # List vision models from a provider
  melange library list --task vision --provider Zetic

  # Inspect a library model
  melange library view zetic/whisper-tiny

  # List the providers
  melange library providers`,
	}

	cmd.AddCommand(newCmdList(f))
	cmd.AddCommand(newCmdView(f))
	cmd.AddCommand(newCmdProviders(f))

	return cmd
}

// genClient returns the generated API client over the authenticated transport.
func genClient(f *cmdutil.Factory) (*gen.ClientWithResponses, error) {
	client, err := f.ApiClient()
	if err != nil {
		return nil, err
	}
	return client.Gen()
}

// splitModelArg parses the required "ACCOUNT/NAME" argument.
func splitModelArg(arg string) (account, name string, err error) {
	parts := strings.Split(arg, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", cmdutil.FlagError{Err: fmt.Errorf(
			"invalid model %q; expected ACCOUNT/NAME", arg)}
	}
	return parts[0], parts[1], nil
}

// deref returns "" for nil string pointers.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// providerName returns the provider's name, or "" when absent.
func providerName(p *gen.LibraryProviderRef) string {
	if p == nil {
		return ""
	}
	return p.Name
}
