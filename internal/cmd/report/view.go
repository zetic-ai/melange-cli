package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// reportKind is one of the three report shapes a model may carry.
type reportKind string

const (
	kindGeneral reportKind = "general"
	kindLLM     reportKind = "llm"
	kindPackage reportKind = "package"
)

// probeOrder is the default --type probe sequence: general, then llm, then
// package. The first that is not a 404 wins.
var probeOrder = []reportKind{kindGeneral, kindLLM, kindPackage}

func newCmdView(f *cmdutil.Factory) *cobra.Command {
	var (
		repo     string
		typ      string
		modeFlag string
		exporter *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "view MODEL_KEY",
		Short: "View a model's benchmark report",
		Long: `Render a model's benchmark report.

--type selects the report: general, llm, or package. Without it the CLI
probes in order — general, then llm, then package — and shows the first
one that exists; when the model has none it exits 1 "no report available".

On a terminal:
  * general — one row per device; a column per (ap_type × precision)
    present, each cell the mode pick's latency in ms (1 decimal). --mode
    (auto, speed, or accuracy) chooses which pick fills the cells; auto is
    the default. Below the table a per-precision summary block lists
    latency min/median/max, the SNR range, and the memory range.
  * llm — rows are devices, columns are quant types, cells are tokens/sec
    (1 decimal); an accuracy section follows, per dataset.
  * package — a mode × metric table.
Missing cells render "-". Devices are sorted alphabetically.

When stdout is not a terminal it prints one raw record per line as
tab-separated values (the flat measurement fields) — scripts get the
records, not the derived table. With --json the API response is emitted
byte-for-byte.

Exit codes: 0 success, 1 API error (including no report), 2 usage error,
4 not authenticated.`,
		Example: `  # The dashboard table (auto-derived mode picks)
  melange report view m_ab12cd -R zetic/whisper-tiny

  # Force the LLM report and fill cells with the speed pick
  melange report view m_ab12cd -R zetic/whisper-tiny --type llm --mode speed

  # Agent pattern: best NPU latency per device, from the raw records
  melange report view m_ab12cd -R zetic/whisper-tiny --json \
    --jq '[.records[] | select(.ap_type=="npu" and .metric=="latency_ms")]
          | group_by(.device.marketing_name)[]
          | {device: .[0].device.marketing_name, best: (map(.value) | min)}'`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := parseMode(modeFlag)
			if err != nil {
				return err
			}
			kinds, err := requestedKinds(cmd, typ)
			if err != nil {
				return err
			}
			account, name, err := splitRepoFlag(repo)
			if err != nil {
				return err
			}
			g, err := genClient(f)
			if err != nil {
				return err
			}
			key := args[0]
			ctx := cmd.Context()

			kind, body, err := fetchReport(ctx, g, kinds, account, name, key, cmd.Flags().Changed("type"))
			if err != nil {
				return err
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(body))
			}

			isTTY := ios.IsStdoutTTY()
			switch kind {
			case kindGeneral:
				return renderGeneral(ios, body, m, isTTY)
			case kindLLM:
				return renderLLM(ios, body, isTTY)
			case kindPackage:
				return renderPackage(ios, body, isTTY)
			}
			return fmt.Errorf("unexpected report kind %q", kind)
		},
	}

	cmd.Flags().StringVarP(&repo, "repo", "R", "", "Repository as `ACCOUNT/REPO` (required)")
	cmd.Flags().StringVar(&typ, "type", "", "Report `type`: general, llm, or package (default: probe)")
	cmd.Flags().StringVar(&modeFlag, "mode", "auto", "Mode pick for general cells: auto, speed, or accuracy")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

// parseMode validates the --mode flag.
func parseMode(s string) (mode, error) {
	switch mode(s) {
	case modeAuto, modeSpeed, modeAccuracy:
		return mode(s), nil
	}
	return "", cmdutil.FlagError{Err: fmt.Errorf(
		"invalid --mode %q; expected auto, speed, or accuracy", s)}
}

// requestedKinds resolves --type to the kinds to try. An explicit --type is a
// single kind (no probing); the default is the full probe order.
func requestedKinds(cmd *cobra.Command, typ string) ([]reportKind, error) {
	if !cmd.Flags().Changed("type") {
		return probeOrder, nil
	}
	switch reportKind(typ) {
	case kindGeneral, kindLLM, kindPackage:
		return []reportKind{reportKind(typ)}, nil
	}
	return nil, cmdutil.FlagError{Err: fmt.Errorf(
		"invalid --type %q; expected general, llm, or package", typ)}
}

// fetchReport requests each kind in turn, returning the first non-404 body.
// With an explicit --type there is a single kind and a 404 surfaces as the
// API error. When probing, all-404 becomes exit 1 "no report available".
func fetchReport(ctx context.Context, g *gen.ClientWithResponses, kinds []reportKind,
	account, name, key string, explicit bool,
) (reportKind, []byte, error) {
	for _, kind := range kinds {
		status, httpResp, body, err := requestReport(ctx, g, kind, account, name, key)
		if err != nil {
			return "", nil, err
		}
		if status == 404 && !explicit {
			continue // probe the next kind.
		}
		if aerr := api.GenError(status, httpResp, body); aerr != nil {
			return "", nil, aerr
		}
		return kind, body, nil
	}
	return "", nil, errors.New("no report available")
}

// requestReport dispatches to the generated client for one report kind.
func requestReport(ctx context.Context, g *gen.ClientWithResponses, kind reportKind,
	account, name, key string,
) (int, *http.Response, []byte, error) {
	switch kind {
	case kindGeneral:
		resp, err := g.GetGeneralReportWithResponse(ctx, account, name, key)
		if err != nil {
			return 0, nil, nil, err
		}
		return resp.StatusCode(), resp.HTTPResponse, resp.Body, nil
	case kindLLM:
		resp, err := g.GetLlmReportWithResponse(ctx, account, name, key)
		if err != nil {
			return 0, nil, nil, err
		}
		return resp.StatusCode(), resp.HTTPResponse, resp.Body, nil
	case kindPackage:
		resp, err := g.GetPackageReportWithResponse(ctx, account, name, key)
		if err != nil {
			return 0, nil, nil, err
		}
		return resp.StatusCode(), resp.HTTPResponse, resp.Body, nil
	}
	return 0, nil, nil, fmt.Errorf("unexpected report kind %q", kind)
}
