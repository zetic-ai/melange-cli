package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func newCmdStatus(f *cmdutil.Factory) *cobra.Command {
	var (
		repo     string
		doWait   bool
		timeout  time.Duration
		exporter *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "status MODEL_KEY",
		Short: "Show a model's conversion status",
		Long: `Show the conversion status of a model: state (converting, optimizing,
ready, failed), the pipeline stage, whether the model is downloadable,
and a sanitized failure code when processing failed.

A plain status read always exits 0 — it is a query. With --wait the
command polls until a terminal state and the exit code reflects the
outcome: 0 when the model is ready, 1 when processing failed or --timeout
elapsed.

On a terminal a human summary is printed; otherwise stable tab-separated
key/value lines. --json preserves API fields and order and adds exactly one
trailing newline.

Exit codes: 0 success, 1 failed outcome under --wait or API error, 2 usage
error, 4 not authenticated.`,
		Example: `  # Resolve the default model key
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results[] | select(.is_default) | .key')

  # Check status once
  melange model status "$model_key" -R zetic/whisper-tiny

  # Block until conversion finishes (up to --timeout)
  melange model status "$model_key" -R zetic/whisper-tiny --wait

  # Agent pattern: just the state
  melange model status "$model_key" -R zetic/whisper-tiny --jq .state`,
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
			key := args[0]

			if doWait {
				return waitForModel(ctx, f, g, account, name, key, timeout, exporter)
			}

			resp, err := g.GetModelStatusWithResponse(ctx, account, name, key)
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			if resp.JSON200 == nil {
				return fmt.Errorf("unexpected response fetching model status (HTTP %d)", resp.StatusCode())
			}
			return printStatus(f, exporter, resp.JSON200, resp.Body, key, account+"/"+name)
		},
	}

	cmd.Flags().StringVarP(&repo, "repo", "R", "", "Repository as `ACCOUNT/REPO` (required)")
	cmd.Flags().BoolVar(&doWait, "wait", false, "Poll until the model reaches a terminal state")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Maximum time to wait with --wait")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

func validateWaitOptions(doWait bool, timeout time.Duration, timeoutSet bool) error {
	if timeoutSet && !doWait {
		return cmdutil.FlagError{Err: fmt.Errorf("--timeout requires --wait")}
	}
	if doWait && timeout <= 0 {
		return cmdutil.FlagError{Err: fmt.Errorf("--timeout must be positive")}
	}
	return nil
}

// printStatus renders a model status: --json raw, TTY human block, or
// stable tab-separated key/value lines.
func printStatus(f *cmdutil.Factory, exporter *cmdutil.Exporter, s *gen.ModelStatusResponse, raw []byte, key, repo string) error {
	ios := f.IOStreams
	if exporter != nil {
		return exporter.Write(ios, json.RawMessage(raw))
	}

	if ios.HumanOutput() {
		state := string(s.State)
		if s.Stage != nil {
			state += fmt.Sprintf(" (stage: %s)", *s.Stage)
		}
		now := time.Now()
		var b strings.Builder
		fmt.Fprintf(&b, "%s in %s\n", key, repo)
		fmt.Fprintf(&b, "State:           %s\n", state)
		fmt.Fprintf(&b, "Terminal:        %s\n", yesNo(s.Terminal))
		fmt.Fprintf(&b, "Download ready:  %s\n", yesNo(s.DownloadReady))
		if fc := deref(s.FailureCode); fc != "" {
			fmt.Fprintf(&b, "Failure code:    %s\n", fc)
		}
		fmt.Fprintf(&b, "Created:         %s\n", text.RelativeTime(s.CreatedAt, now))
		fmt.Fprintf(&b, "Updated:         %s\n", text.RelativeTime(s.UpdatedAt, now))
		_, err := fmt.Fprint(ios.Out, text.SanitizeTerminal(b.String()))
		return err
	}

	// Non-TTY contract: tab-separated key/value lines, stable keys.
	var b strings.Builder
	write := func(k, v string) { b.WriteString(k + "\t" + text.EscapeTSVCell(v) + "\n") }
	write("state", string(s.State))
	if s.Stage != nil {
		write("stage", string(*s.Stage))
	}
	write("terminal", strconv.FormatBool(s.Terminal))
	write("download_ready", strconv.FormatBool(s.DownloadReady))
	if fc := deref(s.FailureCode); fc != "" {
		write("failure_code", fc)
	}
	write("created_at", s.CreatedAt.Format(time.RFC3339))
	write("updated_at", s.UpdatedAt.Format(time.RFC3339))
	_, err := fmt.Fprint(ios.Out, b.String())
	return err
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
