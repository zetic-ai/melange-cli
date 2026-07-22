// Package report implements `melange report` — reading a model's benchmark
// reports (general, LLM, package) as the dashboard-grade table on a terminal
// and as raw, one-record-per-line TSV for scripts.
package report

import (
	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// NewCmdReport builds the `melange report` command group.
func NewCmdReport(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report <command>",
		Short: "Read model benchmark reports",
		Args:  cmdutil.CommandGroupArgs,
		RunE:  cmdutil.ShowCommandGroupHelp,
		Long: `Read the benchmark reports of a converted model: general (per-device
latency/SNR/memory), LLM (per-quant tokens/sec and accuracy), and
package (ZTC per-mode metrics).

On a terminal, ` + "`report view`" + ` renders the dashboard-grade table: the
mode columns are re-derived from the raw records by the pinned selection
rule (speed = lowest latency; accuracy = highest SNR, ties to lower
latency; auto = fastest run whose SNR exceeds 20 dB, else the speed run).
When stdout is not a terminal it prints one raw record per line as
tab-separated values — scripts get the measurements, not the derived
table. With --json the API response is emitted byte-for-byte.

Data is written to stdout; progress and messages go to stderr.`,
		Example: `  # Prefer the public repository's default model; fall back to a ready model
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results | (map(select(.is_default)) + map(select(.state=="ready")) + .)[0].key')

  # The dashboard table for that model's general report
  melange report view "$model_key" -R zetic/whisper-tiny

  # Fill the table with the accuracy-mode pick instead of auto
  melange report view "$model_key" -R zetic/whisper-tiny --mode accuracy

  # Agent pattern: best NPU latency per device, from the raw records
  melange report view "$model_key" -R zetic/whisper-tiny --json \
    --jq '[.records[] | select(.ap_type=="npu" and .metric=="latency_ms")]
          | group_by(.device.marketing_name)[]
          | {device: .[0].device.marketing_name, best: (map(.value) | min)}'`,
	}

	cmd.AddCommand(newCmdView(f))

	return cmd
}
