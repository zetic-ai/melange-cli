// Package plan implements `melange plan` — the account's effective billing
// plan identity (the tier its quotas derive from).
package plan

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// NewCmdPlan builds the `melange plan` command.
func NewCmdPlan(f *cmdutil.Factory) *cobra.Command {
	var exporter *cmdutil.Exporter

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show the account's billing plan",
		Long: `Show the effective billing plan for the token's account: the tier its
quotas derive from (free, lite, pro, pro_plus, or enterprise), whether it is
a trial, and when a trial ends.

The plan reflects what the server actually enforces — an account that bypasses
quota limits reports pro_plus, matching the dashboard. Use "melange usage
quotas" for the per-counter headroom.

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (plan, is_trial,
trial_ends_at; trial_ends_at is empty when not a trial). With --json, API
fields and order are preserved and output ends with exactly one trailing
newline.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # Show the plan
  melange plan

  # Machine-readable
  melange plan --json

  # Agent pattern: the plan tier
  melange plan --jq .plan`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := genClient(f)
			if err != nil {
				return err
			}

			resp, err := g.GetBillingPlanWithResponse(cmd.Context())
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			p := resp.JSON200
			if p == nil {
				return fmt.Errorf("unexpected response fetching plan (HTTP %d)", resp.StatusCode())
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}
			return printPlan(ios, p)
		},
	}

	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

// printPlan renders the plan block for terminals and the tab-separated
// contract for scripts alike (both are key/value; the TTY form aligns keys).
func printPlan(ios *iostreams.IOStreams, p *gen.BillingPlanResponse) error {
	plan := text.SanitizeTerminalInline(string(p.Plan))
	trialEnds := ""
	if p.TrialEndsAt != nil {
		trialEnds = p.TrialEndsAt.Format("2006-01-02T15:04:05Z07:00")
	}

	out := ios.Out
	if ios.IsStdoutTTY() {
		fmt.Fprintf(out, "%-14s %s\n", "Plan:", plan)
		if p.IsTrial {
			if trialEnds != "" {
				fmt.Fprintf(out, "%-14s yes (ends %s)\n", "Trial:", trialEnds)
			} else {
				fmt.Fprintf(out, "%-14s yes\n", "Trial:")
			}
		} else {
			fmt.Fprintf(out, "%-14s no\n", "Trial:")
		}
		return nil
	}
	fmt.Fprintf(out, "plan\t%s\n", plan)
	fmt.Fprintf(out, "is_trial\t%t\n", p.IsTrial)
	fmt.Fprintf(out, "trial_ends_at\t%s\n", trialEnds)
	return nil
}

// genClient returns the generated API client over the authenticated transport.
func genClient(f *cmdutil.Factory) (*gen.ClientWithResponses, error) {
	client, err := f.ApiClient()
	if err != nil {
		return nil, err
	}
	return client.Gen()
}
