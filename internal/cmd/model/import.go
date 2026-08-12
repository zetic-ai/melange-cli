package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func newCmdImport(f *cmdutil.Factory) *cobra.Command {
	var (
		repo       string
		doWait     bool
		ztcPackage bool
		timeout    time.Duration
		exporter   *cmdutil.Exporter
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

Each invocation carries a fresh Idempotency-Key so transient failures can be
retried automatically within that invocation without creating a second import.
Running the command again starts a new import request.
Pinning a HuggingFace revision is not supported yet: imports always use
the repository's current default-branch head.

On success a confirmation with the model key, version, and state goes
to stderr. Without --wait, --json preserves the import response bytes except
for normalizing the terminator to exactly one trailing newline. With --wait,
structured output is
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
			if ztcPackage && doWait {
				return cmdutil.FlagError{Err: errors.New(
					"--wait is not supported with --ztc-package; poll with \"melange model status\" instead")}
			}
			account, name, err := splitRepoFlag(repo)
			if err != nil {
				return err
			}
			if ztcPackage {
				client, err := f.ApiClient()
				if err != nil {
					return err
				}
				imported, raw, err := client.ImportModelZtcPackage(cmd.Context(), account, name,
					args[0], api.NewIdempotencyKey())
				if err != nil {
					return err
				}
				ios := f.IOStreams
				fmt.Fprintf(ios.ErrOut, "✓ Import started: model %s version %d (state %s)\n",
					text.SanitizeTerminalInline(imported.Key), imported.Version,
					text.SanitizeTerminalInline(string(imported.State)))
				if exporter != nil {
					return exporter.Write(ios, json.RawMessage(raw))
				}
				return nil
			}
			g, err := genClient(f)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			resp, err := g.ImportModelWithResponse(ctx, account, name,
				&gen.ImportModelParams{IdempotencyKey: api.NewIdempotencyKeyParam()},
				gen.ImportModelJSONRequestBody{HfRepo: args[0]})
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			imported := resp.JSON201
			if imported == nil {
				imported = resp.JSON200 // The same invocation's Idempotency-Key retry.
			}
			if imported == nil {
				return fmt.Errorf("unexpected response importing model (HTTP %d)", resp.StatusCode())
			}

			ios := f.IOStreams
			fmt.Fprintf(ios.ErrOut, "✓ Import started: model %s version %d (state %s)\n",
				text.SanitizeTerminalInline(imported.Key), imported.Version,
				text.SanitizeTerminalInline(string(imported.State)))

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
	cmd.Flags().BoolVar(&ztcPackage, "ztc-package", false, "Use the non-llama.cpp ZTC package conversion path (staff only; --wait unsupported)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Maximum time to wait with --wait")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}
