// Package plan implements `melange plan` — the account's effective billing
// plan identity (the tier its quotas derive from).
package plan

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// NewCmdPlan builds the `melange plan` command.
func NewCmdPlan(f *cmdutil.Factory) *cobra.Command {
	var exporter *cmdutil.Exporter

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show the account's billing plan",
		Long: `Show the effective billing identity for the token's account. Two
vocabularies coexist:

"plan" is the legacy tier the account's quotas derive from (free, lite,
pro, pro_plus, or enterprise). It reflects what the server actually
enforces — an account that bypasses quota limits reports pro_plus,
matching the dashboard. Use "melange usage quotas" for the per-counter
headroom.

"tier" is the current pricing identity (free, pro, team, or enterprise).
It is null on accounts still on legacy billing; "billing_generation" says
which system governs the account (legacy or v3).

"max_model_bytes" is the plan's own cap on a custom model's total bytes.
It preflights only that size entitlement — other billing checks (credits,
debt, subscription state) are enforced separately at conversion time.

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (plan, is_trial,
trial_ends_at, billing_generation, tier, max_model_bytes; trial_ends_at
is empty when not a trial, tier and max_model_bytes are empty when null).
With --json, API fields and order are preserved and output ends with
exactly one trailing newline.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # Show the plan
  melange plan

  # Machine-readable
  melange plan --json

  # Agent pattern: the legacy plan tier
  melange plan --jq .plan

  # Agent pattern: the pricing identity (null on legacy billing)
  melange plan --jq .tier`,
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

	generation := text.SanitizeTerminalInline(string(p.BillingGeneration))
	tier := ""
	if p.Tier != nil {
		tier = text.SanitizeTerminalInline(string(*p.Tier))
	}
	maxBytes := ""
	if p.MaxModelBytes != nil {
		maxBytes = strconv.Itoa(*p.MaxModelBytes)
	}

	out := ios.Out
	if ios.HumanOutput() {
		trial := "no"
		switch {
		case p.IsTrial && trialEnds != "":
			trial = fmt.Sprintf("yes (ends %s)", trialEnds)
		case p.IsTrial:
			trial = "yes"
		}
		fields := tableprinter.NewFields(ios)
		fields.Add("Plan", plan)
		fields.Add("Trial", trial)
		if tier != "" {
			fields.Add("Tier", tier)
		}
		fields.Add("Billing generation", generation)
		fields.Add("Max model bytes", maxBytes)
		return fields.Render()
	}
	fmt.Fprintf(out, "plan\t%s\n", plan)
	fmt.Fprintf(out, "is_trial\t%t\n", p.IsTrial)
	fmt.Fprintf(out, "trial_ends_at\t%s\n", trialEnds)
	fmt.Fprintf(out, "billing_generation\t%s\n", generation)
	fmt.Fprintf(out, "tier\t%s\n", tier)
	fmt.Fprintf(out, "max_model_bytes\t%s\n", maxBytes)
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
