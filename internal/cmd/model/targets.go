package model

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func newCmdTargets(f *cmdutil.Factory) *cobra.Command {
	var (
		repo     string
		exporter *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "targets MODEL_KEY",
		Short: "List a model's converted targets",
		Long: `List the converted target artifacts of a model, newest first. Each
target is identified by an opaque, stable TARGET_ID — pass it to
"melange model download --target".

On a terminal this prints a table (TARGET_ID, KIND, PRECISION, QUANT,
COMPATIBILITY, SIZE) with human-readable sizes; COMPATIBILITY is a
compact soc/os string, or "-" when the target carries no device
compatibility (LLM targets). PRECISION is "-" for LLM targets, whose
QUANT states the numeric format instead. The runtime engine that built
an artifact is not published: choose between targets on PRECISION,
QUANT and COMPATIBILITY. When stdout is not a terminal, rows are
tab-separated with sizes in raw bytes and no header. With --json, all API
target metadata is preserved and output ends with exactly one trailing
newline.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # Resolve the default model key
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results[] | select(.is_default) | .key')

  # List that model's targets
  melange model targets "$model_key" -R zetic/whisper-tiny

  # Full detail including the compatibility object
  melange model targets "$model_key" -R zetic/whisper-tiny --json

  # Agent pattern: pick the target id for a quant type
  melange model targets "$model_key" -R zetic/whisper-tiny --jq '.results[] | select(.quant_type == "q4_k_m") | .target_id'`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			account, name, err := splitRepoFlag(repo)
			if err != nil {
				return err
			}
			g, err := genClient(f)
			if err != nil {
				return err
			}

			resp, err := g.ListModelTargetsWithResponse(cmd.Context(), account, name, args[0])
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			if resp.JSON200 == nil {
				return fmt.Errorf("unexpected response listing targets (HTTP %d)", resp.StatusCode())
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}
			targets := resp.JSON200.Results
			if len(targets) == 0 {
				if ios.HumanOutput() {
					fmt.Fprintln(ios.ErrOut, "No targets found")
				}
				return nil
			}

			human := ios.HumanOutput()
			tp := tableprinter.New(ios)
			tp.HeaderRow("target_id", "kind", "precision", "quant", "compatibility", "size")
			for _, tgt := range targets {
				tp.AddField(tgt.TargetId)
				tp.AddField(string(tgt.Kind))
				tp.AddField(orDash(precisionString(tgt.Precision)))
				tp.AddField(orDash(deref(tgt.QuantType)))
				tp.AddField(compatString(tgt.Compatibility))
				if human {
					tp.AddField(text.FormatBytes(int64(tgt.DownloadSize)))
				} else {
					tp.AddField(strconv.Itoa(tgt.DownloadSize))
				}
				tp.EndRow()
			}
			tp.Caption(text.Pluralize(len(targets), "target", "targets"))
			return tp.Render()
		},
	}

	cmd.Flags().StringVarP(&repo, "repo", "R", "", "Repository as `ACCOUNT/REPO` (required)")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

// compatString compacts a compatibility object into "soc/os" for tables;
// "-" when the target carries none. The full object is in --json.
func compatString(c *gen.ModelTargetCompatibility) string {
	if c == nil {
		return "-"
	}
	soc := deref(c.SocModel)
	if soc == "" {
		soc = deref(c.SocManufacturer)
	}
	parts := []string{}
	if soc != "" {
		parts = append(parts, soc)
	}
	if os := deref(c.Os); os != "" {
		parts = append(parts, os)
	}
	if len(parts) == 0 {
		return "-"
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "/" + p
	}
	return out
}

// precisionString renders the nullable precision. LLM targets carry none —
// their quant_type states the numeric format instead.
func precisionString(p *gen.ModelTargetItemPrecision) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

// orDash returns "-" for empty strings so table cells stay visibly aligned.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
