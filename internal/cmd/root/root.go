// Package root wires the top-level `melange` cobra command.
package root

import (
	"fmt"

	"github.com/spf13/cobra"
	apicmd "github.com/zetic-ai/melange-cli/internal/cmd/api"
	"github.com/zetic-ai/melange-cli/internal/cmd/auth"
	"github.com/zetic-ai/melange-cli/internal/cmd/model"
	"github.com/zetic-ai/melange-cli/internal/cmd/repo"
	"github.com/zetic-ai/melange-cli/internal/cmd/version"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// NewCmdRoot builds the root cobra.Command for the melange CLI.
func NewCmdRoot(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "melange <command> <subcommand> [flags]",
		Short: "melange — on-device AI model deployment & benchmarking",
		Long: `melange is the command-line interface for the Zetic.ai Melange platform,
which lets you deploy, benchmark, and manage on-device AI models.

Authenticate by setting MELANGE_API_KEY or by running melange auth login.
Data is written to stdout; progress and diagnostics go to stderr.
Exit codes: 0 success, 1 error, 2 usage/flag error, 4 auth error, 130 interrupted.

Reference topics: melange help environment, melange help exit-codes,
melange help formatting.`,

		Example: `  # List repositories as JSON
  melange repo list --json

  # Upload a model and wait for conversion
  melange model upload -R acme/whisper model.onnx --input x.npy --wait

  # Call any API endpoint and extract a value
  melange api /v1/me --jq .account.name`,

		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Wrap cobra's flag parse errors into FlagError so exit-code mapping works.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return cmdutil.FlagError{Err: fmt.Errorf("%w\nRun '%s --help' for usage", err, c.CommandPath())}
	})

	// Persistent flags
	pf := cmd.PersistentFlags()

	var noColor bool
	pf.BoolVar(&noColor, "no-color", false, "Disable color output")

	var noInput bool
	pf.BoolVar(&noInput, "no-input", false, "Disable interactive prompts")

	var host string
	pf.StringVar(&host, "host", "", "Override the Melange API host")
	if err := pf.MarkHidden("host"); err != nil {
		// Not fatal — the flag is still functional.
		_ = err
	}

	// Wire flag values into the factory after flag parse. PersistentPreRunE is
	// per-command state (unlike cobra.OnInitialize, which appends to a package
	// global and would stack stale callbacks across NewCmdRoot calls in tests).
	cmd.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		if noColor {
			f.IOStreams.SetNoColor(true)
		}
		f.NoInput = noInput
		f.HostOverride = host
		return nil
	}

	// Register subcommands.
	cmd.AddCommand(version.NewCmdVersion(f))
	cmd.AddCommand(auth.NewCmdAuth(f))
	cmd.AddCommand(repo.NewCmdRepo(f))
	cmd.AddCommand(model.NewCmdModel(f))
	cmd.AddCommand(apicmd.NewCmdAPI(f))

	// Additional help topics (gh-style): hidden, non-runnable commands that
	// only print reference documentation via `melange help <topic>`.
	for _, topic := range helpTopics {
		cmd.AddCommand(newHelpTopic(topic))
	}

	return cmd
}
