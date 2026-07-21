package model

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
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
--json the resulting model summary is written to stdout exactly as the
API returned it.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # Set the default model
  melange model set-default m_ab12cd -R zetic/whisper-tiny

  # Machine-readable result
  melange model set-default m_ab12cd -R zetic/whisper-tiny --json

  # Agent pattern: confirm the default flag stuck
  melange model set-default m_ab12cd -R zetic/whisper-tiny --json --jq .is_default`,
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
				m.Key, m.Version, account, name)
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
