package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// paginatePageSize is the server page size used by --paginate.
const paginatePageSize = 100

// pageEnvelope mirrors the known keys of the paginated list envelope while
// keeping each result's bytes exactly as the API returned them. The merge in
// --paginate additionally carries every unknown envelope key through verbatim.
type pageEnvelope struct {
	Results []json.RawMessage `json:"results"`
	Count   int               `json:"count"`
}

func newCmdList(f *cmdutil.Factory) *cobra.Command {
	var (
		repo     string
		limit    int
		paginate bool
		all      bool
		exporter *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List models in a repository",
		Long: `List the models of a repository, newest first.

On a terminal this prints a table (KEY, VERSION, TYPE, STATE, DEFAULT,
CREATED) with the state colored (ready green, failed red) and ✓ marking
the repository's default model. When stdout is not a terminal it prints
one model per line as tab-separated values (key, version, type, state,
is_default as true/false, RFC 3339 created_at) with no header — stable
for scripts. With --json the API page envelope {"results": [...], "count": N}
is preserved and followed by exactly one trailing newline; --paginate merges
all pages into one envelope.

An empty result exits 0: a terminal gets "No models found" on stderr,
scripts get empty stdout, --json gets the envelope with empty results.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # List the models of a repository
  melange model list -R zetic/whisper-tiny

  # Fetch every page as JSON
  melange model list -R zetic/whisper-tiny --paginate --json

  # Agent pattern: the key of the default model
  melange model list -R zetic/whisper-tiny --jq '.results[] | select(.is_default) | .key'`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paginate = paginate || all
			if paginate && cmd.Flags().Changed("limit") {
				return cmdutil.FlagError{Err: errors.New("cannot use --limit with --paginate")}
			}
			if !paginate {
				if err := cmdutil.ValidatePageLimit(limit); err != nil {
					return err
				}
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

			fetch := func(limit int, offset *int) (*gen.ListModelsResult, error) {
				params := &gen.ListModelsParams{Limit: &limit, Offset: offset}
				resp, err := g.ListModelsWithResponse(ctx, account, name, params)
				if err != nil {
					return nil, err
				}
				if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
					return nil, aerr
				}
				if resp.JSON200 == nil {
					return nil, fmt.Errorf("unexpected response listing models (HTTP %d)", resp.StatusCode())
				}
				return resp, nil
			}

			var models []gen.ModelSummary
			var envelope json.RawMessage

			if paginate {
				mergedResults := []json.RawMessage{}
				// The last page's envelope, key by key: unknown keys the
				// server may add survive the merge verbatim.
				var envelopeKeys map[string]json.RawMessage
				for offset := 0; ; {
					resp, err := fetch(paginatePageSize, &offset)
					if err != nil {
						return err
					}
					var page pageEnvelope
					if err := json.Unmarshal(resp.Body, &page); err != nil {
						return fmt.Errorf("decoding model page: %w", err)
					}
					// Reset to keep the LAST page only, not a merge of all
					// pages (a fresh map, so a pathological "null" page —
					// which json leaves untouched — cannot leave it nil).
					envelopeKeys = map[string]json.RawMessage{}
					if err := json.Unmarshal(resp.Body, &envelopeKeys); err != nil {
						return fmt.Errorf("decoding model page: %w", err)
					}
					models = append(models, resp.JSON200.Results...)
					mergedResults = append(mergedResults, page.Results...)
					offset += len(page.Results)
					if len(page.Results) == 0 || offset >= page.Count {
						break
					}
				}
				results, err := json.Marshal(mergedResults)
				if err != nil {
					return err
				}
				envelopeKeys["results"] = results
				envelope, err = json.Marshal(envelopeKeys)
				if err != nil {
					return err
				}
			} else {
				resp, err := fetch(limit, nil)
				if err != nil {
					return err
				}
				models = resp.JSON200.Results
				envelope = resp.Body
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, envelope)
			}
			if len(models) == 0 {
				if ios.IsStdoutTTY() {
					fmt.Fprintln(ios.ErrOut, "No models found")
				}
				return nil
			}

			isTTY := ios.IsStdoutTTY()
			cs := ios.ColorScheme()
			now := time.Now()
			tp := tableprinter.New(ios)
			tp.HeaderRow("key", "version", "type", "state", "default", "created")
			for _, m := range models {
				tp.AddField(m.Key)
				tp.AddField(strconv.Itoa(m.Version))
				tp.AddField(m.Type)
				tp.AddField(string(m.State), tableprinter.WithColor(stateColor(cs, string(m.State))))
				if isTTY {
					tp.AddField(defaultMark(m.IsDefault))
				} else {
					tp.AddField(strconv.FormatBool(m.IsDefault))
				}
				if isTTY {
					tp.AddField(text.RelativeTime(m.CreatedAt, now))
				} else {
					tp.AddField(m.CreatedAt.Format(time.RFC3339))
				}
				tp.EndRow()
			}
			return tp.Render()
		},
	}

	cmd.Flags().StringVarP(&repo, "repo", "R", "", "Repository as `ACCOUNT/REPO` (required)")
	cmd.Flags().IntVar(&limit, "limit", 30, "Maximum number of models to fetch (1-100)")
	cmd.Flags().BoolVar(&paginate, "paginate", false, "Fetch all pages of results")
	cmd.Flags().BoolVar(&all, "all", false, "Alias for --paginate")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

// stateColor maps a model state to its table color: ready green, failed red,
// everything else uncolored.
func stateColor(cs *iostreams.ColorScheme, state string) func(string) string {
	switch state {
	case "ready":
		return cs.Green
	case "failed":
		return cs.Red
	}
	return func(s string) string { return s }
}

// defaultMark renders is_default for terminals.
func defaultMark(isDefault bool) string {
	if isDefault {
		return "✓"
	}
	return ""
}
