package model

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func newCmdSetDefault(f *cmdutil.Factory) *cobra.Command {
	var (
		repo     string
		exporter *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "set-default MODEL_KEY",
		Short: "Make a model the repository default",
		Long: `Make this model the repository's default. Exactly one model per
repository is the default; setting a new one clears the previous.

The operation is idempotent: repeating it returns the same result.

On success a confirmation goes to stderr and stdout stays empty; with
--json, API fields and order are preserved and output ends with exactly one
trailing newline.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # Select a model key from the repository
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results[0].key')

  # Set that model as the default
  melange model set-default "$model_key" -R zetic/whisper-tiny

  # Machine-readable result
  melange model set-default "$model_key" -R zetic/whisper-tiny --json

  # Agent pattern: confirm the default flag stuck
  melange model set-default "$model_key" -R zetic/whisper-tiny --json --jq .is_default`,
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
			key := args[0]

			resp, err := g.SetDefaultModelWithResponse(cmd.Context(), account, name, key)
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			m := resp.JSON200
			if m == nil {
				return fmt.Errorf("unexpected response setting default model (HTTP %d)", resp.StatusCode())
			}

			ios := f.IOStreams
			fmt.Fprintf(ios.ErrOut, "✓ Set %s (version %d) as the default model for %s/%s\n",
				text.SanitizeTerminalInline(m.Key), m.Version,
				text.SanitizeTerminalInline(account), text.SanitizeTerminalInline(name))
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&repo, "repo", "R", "", "Repository as `ACCOUNT/REPO` (required)")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}
