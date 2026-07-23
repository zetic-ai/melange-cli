package library

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
)

func newCmdProviders(f *cmdutil.Factory) *cobra.Command {
	var exporter *cmdutil.Exporter

	cmd := &cobra.Command{
		Use:   "providers",
		Short: "List library providers",
		Long: `List the providers (companies) that publish models to the library,
with each provider's model count.

On a terminal this prints a table (NAME, MODELS). When stdout is not a
terminal it prints one provider per line as tab-separated values (name,
model_count) with no header. With --json the API envelope {"results": [...],
"count": N} is preserved and followed by exactly one trailing newline.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # List providers
  melange library providers

  # Agent pattern: provider names with at least 10 models
  melange library providers --jq '.results[] | select(.model_count >= 10) | .name'

  # Machine-readable
  melange library providers --json`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := genClient(f)
			if err != nil {
				return err
			}

			resp, err := g.ListLibraryProvidersWithResponse(cmd.Context())
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			if resp.JSON200 == nil {
				return fmt.Errorf("unexpected response listing providers (HTTP %d)", resp.StatusCode())
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}

			providers := resp.JSON200.Results
			if len(providers) == 0 {
				if ios.IsStdoutTTY() {
					fmt.Fprintln(ios.ErrOut, "No providers found")
				}
				return nil
			}

			tp := tableprinter.New(ios)
			tp.HeaderRow("name", "models")
			for _, p := range providers {
				tp.AddField(p.Name)
				tp.AddField(strconv.Itoa(p.ModelCount))
				tp.EndRow()
			}
			return tp.Render()
		},
	}

	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}
