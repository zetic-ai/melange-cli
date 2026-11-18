// Package program implements `melange program` — reading the benchmark
// sheets of a partner program (an access-code program).
package program

import (
	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// NewCmdProgram builds the `melange program` command group.
func NewCmdProgram(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "program <command>",
		Short: "Read partner program benchmark sheets",
		Args:  cmdutil.CommandGroupArgs,
		RunE:  cmdutil.ShowCommandGroupHelp,
		Long: `Read the benchmark sheets of a partner program: results per SoC for
the program's models, measured and published by Zetic. Only program members
and Zetic staff can read a program.

List the programs you belong to with:
  melange api /v1/me --jq '.access_programs[]?.name'
Zetic staff can read every program without joining one, so this list can be
empty for them; staff pass the program name directly.

Data is written to stdout; progress and messages go to stderr. All
subcommands support --json, --jq, and --template for structured output.`,
		Example: `  # The programs you belong to
  melange api /v1/me --jq '.access_programs[]?.name'

  # Every sheet of one of them
  melange program benchmarks PROGRAM

  # One model on one SoC, as JSON
  melange program benchmarks PROGRAM --model MODEL_SLUG --soc SOC --json`,
	}

	cmd.AddCommand(newCmdBenchmarks(f))

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
