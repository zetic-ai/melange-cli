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
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
)

func newCmdQuotas(f *cmdutil.Factory) *cobra.Command {
	var exporter *cmdutil.Exporter

	cmd := &cobra.Command{
		Use:   "quotas",
		Short: "Show usage against plan limits",
		Long: `Show your current-period usage against your plan limits: active
devices, bandwidth, model uploads, and prompts, plus the account's
benchmark-credit balance.

Each quota renders as "used/limit (pct%)"; a null limit renders as
"unlimited". On a terminal this prints a human-readable block. When
stdout is not a terminal it prints stable tab-separated key/value lines
(each value the same "used/limit (pct%)" or "unlimited" string, followed
by credits_available, credits_reserved, credits_outstanding_debt,
credits_monthly_credits, credits_expiring_credits, and credits_expiring_at
lines; nullable values are empty when null). With --json, API fields and
order are preserved and output ends with exactly one trailing newline.

Each counter also carries a "remaining" field in --json: the amount the
server would actually allow right now (spike headroom included, floored at
0; null means unlimited). Prefer "remaining" over deriving limit-used for
preflight checks — it reflects what enforcement permits.

The "credits" balance is ADVISORY: "model_uploads.remaining > 0" and
"credits.available > 0" are necessary but not sufficient for the next
conversion — "credits.outstanding_debt" must also be 0, and the
per-conversion charge grows with the model's size. When the balance cannot
cover the charge, the conversion is refused with HTTP 402
"credit_balance_exhausted" (nothing is charged and an upload session stays
resumable).

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # Show quotas
  melange usage quotas

  # Machine-readable
  melange usage quotas --json

  # Agent pattern: advisory conversion preflight (headroom, credits, no debt)
  melange usage quotas --jq '{uploads: .model_uploads.remaining,
    credits: .credits.available, debt: .credits.outstanding_debt}'`,
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
	if ios.HumanOutput() {
		p := tableprinter.NewFields(ios)
		for _, r := range rows {
			p.Add(r.label, formatQuota(r.item))
		}
		p.Add("Credits", formatCredits(q.Credits))
		return p.Render()
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s\t%s\n", r.key, formatQuota(r.item))
	}
	c := q.Credits
	monthly := ""
	if c.MonthlyCredits != nil {
		monthly = strconv.Itoa(*c.MonthlyCredits)
	}
	expiringAt := ""
	if c.ExpiringAt != nil {
		expiringAt = c.ExpiringAt.Format("2006-01-02T15:04:05Z07:00")
	}
	fmt.Fprintf(&b, "credits_available\t%d\n", c.Available)
	fmt.Fprintf(&b, "credits_reserved\t%d\n", c.Reserved)
	fmt.Fprintf(&b, "credits_outstanding_debt\t%d\n", c.OutstandingDebt)
	fmt.Fprintf(&b, "credits_monthly_credits\t%s\n", monthly)
	fmt.Fprintf(&b, "credits_expiring_credits\t%d\n", c.ExpiringCredits)
	fmt.Fprintf(&b, "credits_expiring_at\t%s\n", expiringAt)
	_, err := fmt.Fprint(ios.Out, b.String())
	return err
}

// formatCredits renders the advisory credit balance as
// "N available (N reserved[, N expiring YYYY-MM-DD][, N debt outstanding])".
func formatCredits(c gen.CreditsBalance) string {
	details := fmt.Sprintf("%d reserved", c.Reserved)
	if c.ExpiringCredits > 0 && c.ExpiringAt != nil {
		details += fmt.Sprintf(", %d expiring %s", c.ExpiringCredits, c.ExpiringAt.Format("2006-01-02"))
	}
	if c.OutstandingDebt > 0 {
		details += fmt.Sprintf(", %d debt outstanding", c.OutstandingDebt)
	}
	return fmt.Sprintf("%d available (%s)", c.Available, details)
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
