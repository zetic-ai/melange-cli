package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// paginatePageSize is the server page size used by --paginate.
const paginatePageSize = 100

// validTasks is the accepted --task set; anything else is a usage error.
var validTasks = map[string]gen.ListLibraryModelsParamsTask{
	"vision": gen.ListLibraryModelsParamsTaskVision,
	"llm":    gen.ListLibraryModelsParamsTaskLlm,
	"nlp":    gen.ListLibraryModelsParamsTaskNlp,
	"speech": gen.ListLibraryModelsParamsTaskSpeech,
	"other":  gen.ListLibraryModelsParamsTaskOther,
}

// pageEnvelope mirrors the known keys of the paginated list envelope while
// keeping each result's bytes exactly as the API returned them.
type pageEnvelope struct {
	Results []json.RawMessage `json:"results"`
	Count   int               `json:"count"`
}

func newCmdList(f *cmdutil.Factory) *cobra.Command {
	var (
		tasks    []string
		search   string
		provider string
		limit    int
		paginate bool
		all      bool
		exporter *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List public library models",
		Long: `List models in the public library, filtered by task, search text, or
provider.

--task may be repeated; a model matching ANY given task is included
(vision, llm, nlp, speech, other). --search is a case- and separator-insensitive
substring match on name or full_name (hyphens, underscores, slashes, and spaces
are ignored). --provider is an exact provider name.

On a terminal this prints a table (MODEL, PROVIDER, TASK, TYPE, CREATED).
When stdout is not a terminal it prints one model per line as tab-separated
values (full_name, provider, use_case, model_type, RFC 3339 created_at)
with no header — stable for scripts. With --json the API page envelope
{"results": [...], "count": N} is preserved and followed by exactly one
trailing newline; --paginate merges all pages into one envelope.

An empty result exits 0: a terminal gets "No models found" on stderr,
scripts get empty stdout, --json gets the envelope with empty results.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # Speech models from a provider
  melange library list --task speech --provider Zetic

  # Search across every page
  melange library list --search whisper --paginate

  # Agent pattern: just the full names
  melange library list --jq '.results[].full_name'`,
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
			taskParams, err := parseTasks(tasks)
			if err != nil {
				return err
			}

			g, err := genClient(f)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			fetch := func(limit int, offset *int) (*gen.ListLibraryModelsResult, error) {
				params := &gen.ListLibraryModelsParams{Limit: &limit, Offset: offset}
				if len(taskParams) > 0 {
					params.Task = &taskParams
				}
				if search != "" {
					params.Search = &search
				}
				if provider != "" {
					params.Provider = &provider
				}
				resp, err := g.ListLibraryModelsWithResponse(ctx, params)
				if err != nil {
					return nil, err
				}
				if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
					return nil, aerr
				}
				if resp.JSON200 == nil {
					return nil, fmt.Errorf("unexpected response listing library models (HTTP %d)", resp.StatusCode())
				}
				return resp, nil
			}

			var models []gen.LibraryModelItem
			var envelope json.RawMessage

			if paginate {
				mergedResults := []json.RawMessage{}
				var envelopeKeys map[string]json.RawMessage
				for offset := 0; ; {
					resp, err := fetch(paginatePageSize, &offset)
					if err != nil {
						return err
					}
					var page pageEnvelope
					if err := json.Unmarshal(resp.Body, &page); err != nil {
						return fmt.Errorf("decoding library page: %w", err)
					}
					envelopeKeys = map[string]json.RawMessage{}
					if err := json.Unmarshal(resp.Body, &envelopeKeys); err != nil {
						return fmt.Errorf("decoding library page: %w", err)
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
				if ios.HumanOutput() {
					fmt.Fprintln(ios.ErrOut, "No models found")
				}
				return nil
			}

			human := ios.HumanOutput()
			now := time.Now()
			tp := tableprinter.New(ios)
			tp.HeaderRow("model", "provider", "task", "type", "created")
			for _, m := range models {
				tp.AddField(m.FullName)
				tp.AddField(providerName(m.Provider))
				tp.AddField(deref(m.UseCase))
				tp.AddField(m.ModelType)
				if human {
					tp.AddField(text.RelativeTime(m.CreatedAt, now))
				} else {
					tp.AddField(m.CreatedAt.Format(time.RFC3339))
				}
				tp.EndRow()
			}
			tp.Caption(text.Pluralize(len(models), "model", "models"))
			return tp.Render()
		},
	}

	cmd.Flags().StringArrayVar(&tasks, "task", nil, "Filter by use-case `task` (repeatable): vision, llm, nlp, speech, other")
	cmd.Flags().StringVar(&search, "search", "", "Case- and separator-insensitive substring match on `name or full_name`")
	cmd.Flags().StringVar(&provider, "provider", "", "Exact provider `name`")
	cmd.Flags().IntVar(&limit, "limit", 30, "Maximum number of models to fetch (1-100)")
	cmd.Flags().BoolVar(&paginate, "paginate", false, "Fetch all pages of results")
	cmd.Flags().BoolVar(&all, "all", false, "Alias for --paginate")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

// parseTasks maps --task values to the generated enum, rejecting unknown ones.
func parseTasks(tasks []string) ([]gen.ListLibraryModelsParamsTask, error) {
	out := make([]gen.ListLibraryModelsParamsTask, 0, len(tasks))
	for _, t := range tasks {
		v, ok := validTasks[t]
		if !ok {
			return nil, cmdutil.FlagError{Err: fmt.Errorf(
				"invalid --task %q; expected vision, llm, nlp, speech, or other", t)}
		}
		out = append(out, v)
	}
	return out, nil
}
