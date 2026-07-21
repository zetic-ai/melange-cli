package repo

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

// pageEnvelope mirrors the paginated list envelope while keeping each result's
// bytes exactly as the API returned them.
type pageEnvelope struct {
	Results []json.RawMessage `json:"results"`
	Count   int               `json:"count"`
}

func newCmdList(f *cmdutil.Factory) *cobra.Command {
	var (
		limit    int
		search   string
		paginate bool
		all      bool
		exporter *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List repositories",
		Long: `List the repositories your token can see.

On a terminal this prints a table (REPO, VISIBILITY, TYPE, UPDATED).
When stdout is not a terminal it prints one repository per line as
tab-separated values (full_name, visibility, model_type, RFC 3339
updated_at) with no header — stable for scripts. With --json the page
envelope {"results": [...], "count": N} is emitted exactly as the API
returned it; --paginate merges all pages into one envelope.

An empty result exits 0: a terminal gets "No repositories found" on
stderr, scripts get empty stdout, --json gets the envelope with empty
results.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.`,
		Example: `  # List your 30 most relevant repositories
  melange repo list

  # Search across every page of results
  melange repo list --search whisper --paginate

  # Agent pattern: just the repository names
  melange repo list --jq '.results[].full_name'`,
		Args: cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			paginate = paginate || all
			if paginate && cmd.Flags().Changed("limit") {
				return cmdutil.FlagError{Err: errors.New("cannot use --limit with --paginate")}
			}
			if !paginate && limit < 1 {
				return cmdutil.FlagError{Err: errors.New("--limit must be at least 1")}
			}

			g, _, err := genClient(f)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			fetch := func(limit int, offset *int) (*gen.ListReposResult, error) {
				params := &gen.ListReposParams{Limit: &limit, Offset: offset}
				if search != "" {
					params.Search = &search
				}
				resp, err := g.ListReposWithResponse(ctx, params)
				if err != nil {
					return nil, err
				}
				if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
					return nil, err
				}
				if resp.JSON200 == nil {
					return nil, fmt.Errorf("unexpected response listing repositories (HTTP %d)", resp.StatusCode())
				}
				return resp, nil
			}

			var repos []gen.RepoResponse
			var envelope json.RawMessage

			if paginate {
				merged := pageEnvelope{Results: []json.RawMessage{}}
				for offset := 0; ; {
					resp, err := fetch(paginatePageSize, &offset)
					if err != nil {
						return err
					}
					var page pageEnvelope
					if err := json.Unmarshal(resp.Body, &page); err != nil {
						return fmt.Errorf("decoding repository page: %w", err)
					}
					repos = append(repos, resp.JSON200.Results...)
					merged.Results = append(merged.Results, page.Results...)
					merged.Count = page.Count
					offset += len(page.Results)
					if len(page.Results) == 0 || offset >= page.Count {
						break
					}
				}
				envelope, err = json.Marshal(merged)
				if err != nil {
					return err
				}
			} else {
				resp, err := fetch(limit, nil)
				if err != nil {
					return err
				}
				repos = resp.JSON200.Results
				envelope = resp.Body
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, envelope)
			}
			if len(repos) == 0 {
				if ios.IsStdoutTTY() {
					fmt.Fprintln(ios.ErrOut, "No repositories found")
				}
				return nil
			}

			isTTY := ios.IsStdoutTTY()
			cs := ios.ColorScheme()
			now := time.Now()
			tp := tableprinter.New(ios)
			tp.HeaderRow("repo", "visibility", "type", "updated")
			for _, r := range repos {
				tp.AddField(r.FullName)
				if r.IsPrivate {
					tp.AddField("private", tableprinter.WithColor(cs.Yellow))
				} else {
					tp.AddField("public")
				}
				tp.AddField(r.ModelType)
				if isTTY {
					tp.AddField(text.RelativeTime(r.UpdatedAt, now))
				} else {
					tp.AddField(r.UpdatedAt.Format(time.RFC3339))
				}
				tp.EndRow()
			}
			return tp.Render()
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 30, "Maximum number of repositories to fetch")
	cmd.Flags().StringVar(&search, "search", "", "Filter repositories by a search `query`")
	cmd.Flags().BoolVar(&paginate, "paginate", false, "Fetch all pages of results")
	cmd.Flags().BoolVar(&all, "all", false, "Alias for --paginate")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}
