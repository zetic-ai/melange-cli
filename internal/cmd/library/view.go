package library

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// readmeExcerptLines is how many leading readme lines the human view shows.
const readmeExcerptLines = 10

func newCmdView(f *cmdutil.Factory) *cobra.Command {
	var exporter *cmdutil.Exporter

	cmd := &cobra.Command{
		Use:   "view ACCOUNT/NAME",
		Short: "View a library model",
		Long: `Show a single public library model: its full name, provider, use-case
task, model type, tags, description, and a readme excerpt (the first 10
lines, noting when it is truncated).

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (full_name,
account, name, provider, use_case, model_type, tags as comma-joined,
description, RFC 3339 created_at; readme omitted). With --json, API fields
and order — including the full readme — are preserved and output ends with
exactly one trailing newline.

Library entries are repository coordinates, not converted model keys. Public
library repositories can be inspected directly, without importing or uploading:

repo=$(melange library list --search QUERY --jq '.results[0].full_name')
key=$(melange model list -R "$repo" --jq '.results | (map(select(.is_default and .state=="ready")) + map(select(.state=="ready")))[0].key // empty')
[ -n "$key" ] || { echo "No ready model is available in $repo" >&2; exit 1; }
melange report view "$key" -R "$repo" --json
melange deploy guide "$key" -R "$repo" --language android-kotlin --mode auto

Never import a library model solely to read its public benchmarks.

Exit codes: 0 success, 1 API error (including not found), 2 usage error,
4 not authenticated.`,
		Example: `  # View a library model
  melange library view zetic/whisper-tiny

  # The full readme as JSON
  melange library view zetic/whisper-tiny --jq .readme

  # Machine-readable detail
  melange library view zetic/whisper-tiny --json`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			account, name, err := splitModelArg(args[0])
			if err != nil {
				return err
			}
			g, err := genClient(f)
			if err != nil {
				return err
			}

			resp, err := g.GetLibraryModelWithResponse(cmd.Context(), account, name)
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			m := resp.JSON200
			if m == nil {
				return fmt.Errorf("unexpected response fetching library model %s/%s (HTTP %d)",
					account, name, resp.StatusCode())
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}
			if ios.HumanOutput() {
				return printModelTTY(f, m)
			}
			return printModelTSV(f, m)
		},
	}

	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

// printModelTTY renders the human block, including a readme excerpt.
func printModelTTY(f *cmdutil.Factory, m *gen.LibraryModelDetailResponse) error {
	now := time.Now()
	p := tableprinter.NewFields(f.IOStreams)
	p.Title(m.FullName)
	p.Add("Provider", providerName(m.Provider))
	p.Add("Task", deref(m.UseCase))
	p.Add("Type", m.ModelType)
	p.Add("Tags", strings.Join(m.Tags, ", "))
	p.Add("Created", text.RelativeTime(m.CreatedAt, now))
	p.Paragraph(deref(m.Description))

	if excerpt, truncated := readmeExcerpt(deref(m.Readme)); excerpt != "" {
		readme := "Readme:\n" + excerpt
		if truncated {
			readme += "\n... (readme truncated; use --json for the full text)"
		}
		p.Paragraph(readme)
	}
	p.Paragraph(fmt.Sprintf(
		"Next: list converted model keys with `%s model list -R %s`.\n"+
			"Then render code with `%s deploy guide MODEL_KEY -R %s`.",
		f.Edition.ProgramName(), m.FullName, f.Edition.ProgramName(), m.FullName))
	return p.Render()
}

// readmeExcerpt returns the first readmeExcerptLines lines of the readme and
// whether more lines followed.
func readmeExcerpt(readme string) (excerpt string, truncated bool) {
	if readme == "" {
		return "", false
	}
	lines := strings.Split(readme, "\n")
	if len(lines) <= readmeExcerptLines {
		return strings.TrimRight(readme, "\n"), false
	}
	return strings.Join(lines[:readmeExcerptLines], "\n"), true
}

// printModelTSV renders the machine contract: stable tab-separated key/value
// lines. The readme is omitted (use --json for the full text).
func printModelTSV(f *cmdutil.Factory, m *gen.LibraryModelDetailResponse) error {
	var b strings.Builder
	write := func(k, v string) { b.WriteString(k + "\t" + text.EscapeTSVCell(v) + "\n") }
	write("full_name", m.FullName)
	write("account", m.Account)
	write("name", m.Name)
	write("provider", providerName(m.Provider))
	write("use_case", deref(m.UseCase))
	write("model_type", m.ModelType)
	write("tags", strings.Join(m.Tags, ","))
	write("description", deref(m.Description))
	write("created_at", m.CreatedAt.Format(time.RFC3339))
	_, err := fmt.Fprint(f.IOStreams.Out, b.String())
	return err
}
