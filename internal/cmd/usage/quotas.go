package usage

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

func newCmdQuotas(f *cmdutil.Factory) *cobra.Command {
	var exporter *cmdutil.Exporter

	cmd := &cobra.Command{
		Use:   "quotas",
		Short: "Show usage against plan limits",
		Long: `Show your current-period usage against your plan limits: active
devices, bandwidth, model uploads, and prompts.

Each quota renders as "used/limit (pct%)"; a null limit renders as
"unlimited". On a terminal this prints a human-readable block. When
stdout is not a terminal it prints stable tab-separated key/value lines
(each value the same "used/limit (pct%)" or "unlimited" string). With
--json the resource object is emitted exactly as the API returned it.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # Show quotas
  melange usage quotas

  # Machine-readable
  melange usage quotas --json

  # Agent pattern: the prompts limit (null when unlimited)
  melange usage quotas --jq .prompts.limit`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := genClient(f)
			if err != nil {
				return err
			}

			resp, err := g.GetUsageQuotasWithResponse(cmd.Context())
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			q := resp.JSON200
			if q == nil {
				return fmt.Errorf("unexpected response fetching quotas (HTTP %d)", resp.StatusCode())
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}
			return printQuotas(ios, q)
		},
	}

	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

// printQuotas renders each quota as "used/limit (pct%)" (or "unlimited").
func printQuotas(ios *iostreams.IOStreams, q *gen.UsageQuotasResponse) error {
	rows := []struct {
		key   string
		label string
		item  gen.QuotaItem
	}{
		{"active_devices", "Active devices", q.ActiveDevices},
		{"bandwidth", "Bandwidth", q.Bandwidth},
		{"model_uploads", "Model uploads", q.ModelUploads},
		{"prompts", "Prompts", q.Prompts},
	}
	var b strings.Builder
	if ios.IsStdoutTTY() {
		for _, r := range rows {
			fmt.Fprintf(&b, "%-16s %s\n", r.label+":", formatQuota(r.item))
		}
	} else {
		for _, r := range rows {
			fmt.Fprintf(&b, "%s\t%s\n", r.key, formatQuota(r.item))
		}
	}
	_, err := fmt.Fprint(ios.Out, b.String())
	return err
}

// formatQuota renders one quota. A nil limit is "unlimited"; otherwise
// "used/limit (pct%)" where pct is the integer percentage used (a zero limit
// yields 0% rather than a division by zero).
func formatQuota(item gen.QuotaItem) string {
	if item.Limit == nil {
		return "unlimited"
	}
	limit := *item.Limit
	pct := 0
	if limit > 0 {
		pct = item.Used * 100 / limit
	}
	return fmt.Sprintf("%s/%s (%d%%)", strconv.Itoa(item.Used), strconv.Itoa(limit), pct)
}
