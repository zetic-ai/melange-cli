package model

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

func newCmdImport(f *cmdutil.Factory) *cobra.Command {
	var (
		repo     string
		doWait   bool
		timeout  time.Duration
		exporter *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "import HF_REPO",
		Short: "Import an LLM from a public HuggingFace repository",
		Long: `Register an LLM model from a public HuggingFace repository (for
example "meta-llama/Llama-3.2-1B"; hf:// and URL prefixes are accepted).

The target repository must have model type llm; other repositories are
rejected by the server. Conversion continues asynchronously — poll it
with "melange model status", or pass --wait to block until it reaches a
terminal state.

The request carries an Idempotency-Key, so transient failures are
retried safely; replaying the same import returns the original model.
Pinning a HuggingFace revision is not supported yet: imports always use
the repository's current default-branch head.

On success a confirmation with the model key, version, and state goes
to stderr. Without --wait, --json writes the import response exactly as
the API returned it. With --wait, structured output is
{"model": <import response>, "status": <final status>}; for example,
--jq .model.key returns the imported model key.

Exit codes: 0 success, 1 API error or failed conversion under --wait,
2 usage error, 4 not authenticated, 130 interrupted.`,
		Example: `  # Import a model, wait, and print its stable model key
  melange model import meta-llama/Llama-3.2-1B -R zetic/llama --wait --jq .model.key

  # Import without waiting
  melange model import meta-llama/Llama-3.2-1B -R zetic/llama

  # Agent pattern: capture the new model key
  melange model import meta-llama/Llama-3.2-1B -R zetic/llama --json --jq .key`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateWaitOptions(doWait, timeout, cmd.Flags().Changed("timeout")); err != nil {
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
			ctx := cmd.Context()

			resp, err := g.ImportModelWithResponse(ctx, account, name,
				&gen.ImportModelParams{IdempotencyKey: newIdempotencyKeyParam()},
				gen.ImportModelJSONRequestBody{HfRepo: args[0]})
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			imported := resp.JSON201
			if imported == nil {
				imported = resp.JSON200 // Idempotency-Key replay of the same import
			}
			if imported == nil {
				return fmt.Errorf("unexpected response importing model (HTTP %d)", resp.StatusCode())
			}

			ios := f.IOStreams
			fmt.Fprintf(ios.ErrOut, "✓ Import started: model %s version %d (state %s)\n",
				imported.Key, imported.Version, imported.State)

			if doWait {
				return waitForModelWithResult(ctx, f, g, account, name, imported.Key,
					timeout, exporter, json.RawMessage(resp.Body))
			}
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&repo, "repo", "R", "", "Target repository as `ACCOUNT/REPO` (required)")
	cmd.Flags().BoolVar(&doWait, "wait", false, "After import, wait until conversion reaches a terminal state")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Maximum time to wait with --wait")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}
