// Package usage implements `melange usage` — the current billing-period usage
// counters and, via `usage quotas`, those counters against the plan limits.
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

// NewCmdUsage builds the `melange usage` command (with a `quotas` subcommand).
func NewCmdUsage(f *cmdutil.Factory) *cobra.Command {
	var exporter *cmdutil.Exporter

	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show current usage counters",
		Long: `Show your usage for the current billing period: active devices,
bandwidth, model uploads, and prompts.

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (active_devices,
bandwidth, model_uploads, prompts). With --json the resource object is
emitted exactly as the API returned it. Use "melange usage quotas" to
see these counters against your plan limits.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # Show usage
  melange usage

  # Machine-readable
  melange usage --json

  # Agent pattern: prompts used this period
  melange usage --jq .prompts`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := genClient(f)
			if err != nil {
				return err
			}

			resp, err := g.GetUsageWithResponse(cmd.Context())
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			u := resp.JSON200
			if u == nil {
				return fmt.Errorf("unexpected response fetching usage (HTTP %d)", resp.StatusCode())
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}
			return printUsage(ios, u)
		},
	}

	cmdutil.AddJSONFlags(cmd, &exporter)
	cmd.AddCommand(newCmdQuotas(f))

	return cmd
}

// printUsage renders the usage block for terminals and the tab-separated
// contract for scripts alike (both are key/value; the TTY form aligns keys).
func printUsage(ios *iostreams.IOStreams, u *gen.UsageResponse) error {
	rows := []struct {
		key   string
		label string
		value int
	}{
		{"active_devices", "Active devices", u.ActiveDevices},
		{"bandwidth", "Bandwidth", u.Bandwidth},
		{"model_uploads", "Model uploads", u.ModelUploads},
		{"prompts", "Prompts", u.Prompts},
	}
	var b strings.Builder
	if ios.IsStdoutTTY() {
		for _, r := range rows {
			fmt.Fprintf(&b, "%-16s %d\n", r.label+":", r.value)
		}
	} else {
		for _, r := range rows {
			fmt.Fprintf(&b, "%s\t%s\n", r.key, strconv.Itoa(r.value))
		}
	}
	_, err := fmt.Fprint(ios.Out, b.String())
	return err
}

// genClient returns the generated API client over the authenticated transport.
func genClient(f *cmdutil.Factory) (*gen.ClientWithResponses, error) {
	client, err := f.ApiClient()
	if err != nil {
		return nil, err
	}
	return client.Gen()
}
