package deploy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

func newCmdOptions(f *cmdutil.Factory) *cobra.Command {
	var exporter *cmdutil.Exporter
	cmd := &cobra.Command{
		Use:   "options",
		Short: "List supported deployment languages and inference modes",
		Long: `List the exact deployment selectors supported by public-v1.
React Native is intentionally excluded. Use --json for the structured contract.`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			g, err := genClient(f)
			if err != nil {
				return err
			}
			resp, err := g.GetDeploymentOptionsWithResponse(cmd.Context())
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			if resp.JSON200 == nil {
				return fmt.Errorf("unexpected response fetching deployment options (HTTP %d)", resp.StatusCode())
			}
			if exporter != nil {
				return exporter.Write(f.IOStreams, json.RawMessage(resp.Body))
			}
			return printOptions(f, resp.JSON200)
		},
	}
	cmdutil.AddJSONFlags(cmd, &exporter)
	return cmd
}

func printOptions(f *cmdutil.Factory, options *gen.DeploymentOptionsResponse) error {
	var b strings.Builder
	fmt.Fprintln(&b, "Languages:")
	for _, language := range options.Languages {
		marker := ""
		if string(language.Id) == string(options.DefaultLanguage) {
			marker = " (default)"
		}
		fmt.Fprintf(&b, "  %-18s %s%s\n", language.Id, language.Label, marker)
	}
	fmt.Fprintln(&b, "Inference modes:")
	for _, mode := range options.InferenceModes {
		marker := ""
		if string(mode.Id) == string(options.DefaultInferenceMode) {
			marker = " (default)"
		}
		fmt.Fprintf(&b, "  %-10s %s%s — %s\n", mode.Id, mode.Label, marker, mode.Description)
	}
	_, err := fmt.Fprint(f.IOStreams.Out, b.String())
	return err
}
