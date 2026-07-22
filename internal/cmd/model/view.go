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

func newCmdView(f *cmdutil.Factory) *cobra.Command {
	var (
		repo     string
		exporter *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "view MODEL_KEY",
		Short: "View a model",
		Long: `Show a single model: key, version, type, state, whether it is the
repository's default, its source (upload or import), the terminal and
download-ready flags, a sanitized failure code when processing failed,
and timestamps.

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (key, version,
type, state, is_default, source_type, terminal, download_ready,
failure_code when present, created_at, updated_at; timestamps in
RFC 3339). With --json the resource object is emitted exactly as the
API returned it.

Exit codes: 0 success, 1 API error (including not found), 2 usage
error, 4 not authenticated.`,
		Example: `  # Resolve the default model key
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results[] | select(.is_default) | .key')

  # View that model
  melange model view "$model_key" -R zetic/whisper-tiny

  # Machine-readable detail
  melange model view "$model_key" -R zetic/whisper-tiny --json

  # Agent pattern: is the model downloadable yet?
  melange model view "$model_key" -R zetic/whisper-tiny --jq .download_ready`,
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

			resp, err := g.GetModelWithResponse(cmd.Context(), account, name, key)
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			m := resp.JSON200
			if m == nil {
				return fmt.Errorf("unexpected response fetching model %s (HTTP %d)", key, resp.StatusCode())
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}
			if ios.IsStdoutTTY() {
				return printModelTTY(f, m, account+"/"+name)
			}
			return printModelTSV(f, m)
		},
	}

	cmd.Flags().StringVarP(&repo, "repo", "R", "", "Repository as `ACCOUNT/REPO` (required)")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

// printModelTTY renders the human block for terminals.
func printModelTTY(f *cmdutil.Factory, m *gen.ModelDetailResponse, repo string) error {
	ios := f.IOStreams
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "%s in %s\n", m.Key, repo)
	fmt.Fprintf(&b, "Version:         %d\n", m.Version)
	fmt.Fprintf(&b, "Type:            %s\n", m.Type)
	fmt.Fprintf(&b, "State:           %s\n", m.State)
	fmt.Fprintf(&b, "Default:         %s\n", yesNo(m.IsDefault))
	fmt.Fprintf(&b, "Source:          %s\n", m.SourceType)
	fmt.Fprintf(&b, "Terminal:        %s\n", yesNo(m.Terminal))
	fmt.Fprintf(&b, "Download ready:  %s\n", yesNo(m.DownloadReady))
	if fc := deref(m.FailureCode); fc != "" {
		fmt.Fprintf(&b, "Failure code:    %s\n", fc)
	}
	fmt.Fprintf(&b, "Created:         %s\n", text.RelativeTime(m.CreatedAt, now))
	fmt.Fprintf(&b, "Updated:         %s\n", text.RelativeTime(m.UpdatedAt, now))
	_, err := fmt.Fprint(ios.Out, b.String())
	return err
}

// printModelTSV renders the machine contract: stable tab-separated key/value
// lines (failure_code only when the server sent one, like model status).
func printModelTSV(f *cmdutil.Factory, m *gen.ModelDetailResponse) error {
	var b strings.Builder
	write := func(k, v string) { b.WriteString(k + "\t" + v + "\n") }
	write("key", m.Key)
	write("version", strconv.Itoa(m.Version))
	write("type", m.Type)
	write("state", string(m.State))
	write("is_default", strconv.FormatBool(m.IsDefault))
	write("source_type", m.SourceType)
	write("terminal", strconv.FormatBool(m.Terminal))
	write("download_ready", strconv.FormatBool(m.DownloadReady))
	if fc := deref(m.FailureCode); fc != "" {
		write("failure_code", fc)
	}
	write("created_at", m.CreatedAt.Format(time.RFC3339))
	write("updated_at", m.UpdatedAt.Format(time.RFC3339))
	_, err := fmt.Fprint(f.IOStreams.Out, b.String())
	return err
}
