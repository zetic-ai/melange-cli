package program

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/programsheet"
)

func newCmdBenchmarks(f *cmdutil.Factory) *cobra.Command {
	var (
		filter   programsheet.Filter
		exporter *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "benchmarks PROGRAM",
		Short: "View a program's benchmark sheets",
		Long: `Show the benchmark sheets of a partner program. PROGRAM is the program
name, as listed in access_programs by melange api /v1/me.

Results are per SoC. Each model's sheet on a SoC lists its builds (Zetic's
runtime, the vendor baseline it is compared against, and usually a float
reference for accuracy) with speed and accuracy metrics. Which
metrics a model has depends on its task (llm, asr, tts,
image_classification); the response's "metrics" object describes each one
(label, unit, which direction is better).

--model keeps one model (by slug) and --soc one SoC; with --soc, models
without a result on that SoC are left out. A model or SoC the program does
not list exits 1 and names the ones it does.

On a terminal this prints, per model and SoC, a table of the builds and the
task's summary metrics. A trailing "(baseline)" marks the build the page
compares against; when only some builds are in the program page's summary,
those are marked "*". Keyed metrics print every key (prompt length or test
set). The measurement method, notes and extra tables are in --json.

When stdout is not a terminal it prints one metric value per line as
tab-separated values: soc, model, task, variant, build, runtime, status,
metric, key, value, unit. key is empty for unkeyed metrics; a build with no
metrics (status unsupported or not_measured) prints one line with empty
metric, key, value and unit. Values are the API's numbers verbatim.

With --json the API response is emitted with its fields and order
preserved (narrowed by --model/--soc), followed by exactly one trailing
newline.

A program you are not a member of answers 404, the same as a program that
does not exist.

Exit codes: 0 success, 1 API error (including not found) or unknown
--model/--soc, 2 usage error, 4 not authenticated.`,
		Example: `  # Every sheet of a program
  melange program benchmarks PROGRAM

  # One model on one SoC
  melange program benchmarks PROGRAM --model MODEL_SLUG --soc SOC

  # The models and SoCs a program lists
  melange program benchmarks PROGRAM --json --jq '{models: [.models[].slug], socs: [.targets[].soc]}'

  # Agent pattern: decode speed of every LLM build, per SoC
  melange program benchmarks PROGRAM --json \
    --jq '.models[] | select(.task=="llm") | .slug as $m | .results[] | .soc as $s
          | .builds[] | {model: $m, soc: $s, build: .key, decode_tps: .speed.decode_tps}'`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			program := args[0]
			g, err := genClient(f)
			if err != nil {
				return err
			}
			resp, err := g.GetProgramBenchmarksWithResponse(cmd.Context(), program)
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			if resp.JSON200 == nil {
				return fmt.Errorf("unexpected response fetching program %s (HTTP %d)",
					program, resp.StatusCode())
			}
			body, err := programsheet.Apply(resp.Body, filter)
			if err != nil {
				return err
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(body))
			}
			page, err := decodePage(body)
			if err != nil {
				return err
			}
			if ios.HumanOutput() {
				return renderTTY(ios, page, f.Edition.ProgramName())
			}
			return renderTSV(ios, page)
		},
	}

	cmd.Flags().StringVar(&filter.Model, "model", "", "Keep one model, by `slug`")
	cmd.Flags().StringVar(&filter.SoC, "soc", "", "Keep one `SoC` from the program's catalog")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}
