// Package root wires the top-level `melange` cobra command.
package root

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	apicmd "github.com/zetic-ai/melange-cli/internal/cmd/api"
	"github.com/zetic-ai/melange-cli/internal/cmd/auth"
	"github.com/zetic-ai/melange-cli/internal/cmd/deploy"
	"github.com/zetic-ai/melange-cli/internal/cmd/library"
	mcpcmd "github.com/zetic-ai/melange-cli/internal/cmd/mcp"
	"github.com/zetic-ai/melange-cli/internal/cmd/model"
	"github.com/zetic-ai/melange-cli/internal/cmd/plan"
	"github.com/zetic-ai/melange-cli/internal/cmd/repo"
	"github.com/zetic-ai/melange-cli/internal/cmd/report"
	"github.com/zetic-ai/melange-cli/internal/cmd/usage"
	"github.com/zetic-ai/melange-cli/internal/cmd/version"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

// NewCmdRoot builds the root cobra.Command for the melange CLI.
func NewCmdRoot(f *cmdutil.Factory) *cobra.Command {
	program := f.Edition.ProgramName()
	short := "melange — on-device AI model deployment & benchmarking"
	long := `melange is the command-line interface for the Zetic.ai Melange platform,
which lets you deploy, benchmark, and manage on-device AI models.

Authenticate by setting MELANGE_API_KEY or by running melange auth login.
Data is written to stdout; progress and diagnostics go to stderr.
Exit codes: 0 success, 1 error, 2 usage/flag error, 4 auth error, 130 interrupted.

Reference topics: melange help environment, melange help exit-codes,
melange help formatting.`
	if f.Edition.IsQualcomm() {
		short = "melange-qcom — Qualcomm-focused on-device AI deployment & benchmarking"
		long = `melange-qcom is the Qualcomm-focused edition of the Zetic.ai Melange CLI.
It shares Melange authentication and model management while curating benchmark
reports, converted targets, and deployment guides for Qualcomm team workflows.

Report and target commands fail closed against the reviewed Qualcomm device
fleet. The raw api command is intentionally unfiltered and is not an enforcement
boundary. Data is written to stdout; progress and diagnostics go to stderr.

Reference topics: melange-qcom help environment,
melange-qcom help exit-codes, melange-qcom help formatting.`
	}
	cmd := &cobra.Command{
		Use:   program + " <command> <subcommand> [flags]",
		Short: short,
		Long:  long,

		Example: fmt.Sprintf(`  # List repositories as JSON
  %s repo list --json

  # Upload a model and wait for conversion
  %s model upload -R acme/whisper model.onnx --input x.npy --wait

  # Call any API endpoint and extract a value
  %s api /v1/me --jq .account.name`, program, program, program),

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

	// Presentation flag, like --no-color: it selects the layout of human output
	// without changing what any command does. Structured output stays under
	// --json/--jq/--template.
	var format string
	pf.StringVar(&format, "format", "auto",
		"Human output layout `auto|table|tsv`; auto means table on a terminal, tab-separated otherwise")

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
		parsed, err := iostreams.ParseFormat(format)
		if err != nil {
			return cmdutil.FlagError{Err: err}
		}
		f.IOStreams.SetFormat(parsed)
		f.NoInput = noInput
		f.HostOverride = host
		return nil
	}

	// Register subcommands.
	cmd.AddCommand(version.NewCmdVersion(f))
	cmd.AddCommand(auth.NewCmdAuth(f))
	cmd.AddCommand(repo.NewCmdRepo(f))
	cmd.AddCommand(model.NewCmdModel(f))
	cmd.AddCommand(deploy.NewCmdDeploy(f))
	cmd.AddCommand(report.NewCmdReport(f))
	cmd.AddCommand(library.NewCmdLibrary(f))
	cmd.AddCommand(usage.NewCmdUsage(f))
	cmd.AddCommand(plan.NewCmdPlan(f))
	cmd.AddCommand(apicmd.NewCmdAPI(f))
	cmd.AddCommand(mcpcmd.NewCmdMCP(f))

	// Additional help topics (gh-style): hidden, non-runnable commands that
	// only print reference documentation via `melange help <topic>`.
	for _, topic := range helpTopics {
		cmd.AddCommand(newHelpTopic(topic))
	}
	if f.Edition.IsQualcomm() {
		applyEditionPresentation(cmd, program)
	}

	return cmd
}

func applyEditionPresentation(cmd *cobra.Command, program string) {
	cmd.Short = rewriteProgramReferences(cmd.Short, program)
	cmd.Long = rewriteProgramReferences(cmd.Long, program)
	cmd.Example = rewriteProgramReferences(cmd.Example, program)
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		flag.Usage = rewriteProgramReferences(flag.Usage, program)
	})
	if cmd.RunE != nil {
		run := cmd.RunE
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			err := run(cmd, args)
			if err == nil {
				return nil
			}
			message := rewriteProgramReferences(err.Error(), program)
			if message == err.Error() {
				return err
			}
			return brandedError{message: message, err: err}
		}
	}
	for _, child := range cmd.Commands() {
		applyEditionPresentation(child, program)
	}
}

type brandedError struct {
	message string
	err     error
}

func (e brandedError) Error() string { return e.message }
func (e brandedError) Unwrap() error { return e.err }

func rewriteProgramReferences(value, program string) string {
	for _, old := range []string{"melange ", "`melange`", "`melange ", `"melange `} {
		replacement := strings.Replace(old, "melange", program, 1)
		value = strings.ReplaceAll(value, old, replacement)
	}
	return value
}
