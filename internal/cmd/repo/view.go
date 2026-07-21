package repo

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func newCmdView(f *cmdutil.Factory) *cobra.Command {
	var exporter *cmdutil.Exporter

	cmd := &cobra.Command{
		Use:   "view <[account/]name>",
		Short: "View a repository",
		Long: `Show a single repository: name, visibility, type, use case, tags,
description, and timestamps.

When ACCOUNT/ is omitted, the repository is looked up under the account
behind your token (one extra /v1/me call).

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (name,
visibility, type, use_case, tags, description, created_at, updated_at;
timestamps in RFC 3339). With --json the resource object is emitted
exactly as the API returned it.

Exit codes: 0 success, 1 API error (including not found), 2 usage
error, 4 not authenticated.`,
		Example: `  # View a repository in your account
  melange repo view whisper-tiny

  # View another account's repository
  melange repo view zetic/whisper-tiny

  # Agent pattern: check whether a repository is private
  melange repo view zetic/whisper-tiny --jq .is_private`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			account, name, err := splitRepoArg(args[0])
			if err != nil {
				return err
			}

			g, client, err := genClient(f)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			if account == "" {
				if account, err = resolveAccount(ctx, client); err != nil {
					return err
				}
			}

			resp, err := g.GetRepoWithResponse(ctx, account, name)
			if err != nil {
				return err
			}
			if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
				return err
			}
			if resp.JSON200 == nil {
				return fmt.Errorf("unexpected response for repository %s/%s (HTTP %d)", account, name, resp.StatusCode())
			}

			ios := f.IOStreams
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}
			if ios.IsStdoutTTY() {
				return printRepoTTY(ios, resp.JSON200)
			}
			return printRepoTSV(ios, resp.JSON200)
		},
	}

	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}

// printRepoTTY renders the human block for terminals.
func printRepoTTY(ios *iostreams.IOStreams, r *gen.RepoResponse) error {
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", r.FullName)

	line := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%-13s%s\n", label+":", value)
		}
	}
	line("Visibility", visibility(r.IsPrivate))
	line("Type", r.ModelType)
	line("Use case", deref(r.UseCase))
	line("Tags", strings.Join(r.Tags, ", "))
	line("Created", text.RelativeTime(r.CreatedAt, now))
	line("Updated", text.RelativeTime(r.UpdatedAt, now))

	if desc := deref(r.Description); desc != "" {
		fmt.Fprintf(&b, "\n%s\n", desc)
	}
	_, err := fmt.Fprint(ios.Out, b.String())
	return err
}

// printRepoTSV renders the machine contract: one "key<TAB>value" line per
// field, always in the same order with every key present.
func printRepoTSV(ios *iostreams.IOStreams, r *gen.RepoResponse) error {
	rows := [][2]string{
		{"name", r.FullName},
		{"visibility", visibility(r.IsPrivate)},
		{"type", r.ModelType},
		{"use_case", deref(r.UseCase)},
		{"tags", strings.Join(r.Tags, ",")},
		{"description", deref(r.Description)},
		{"created_at", r.CreatedAt.Format(time.RFC3339)},
		{"updated_at", r.UpdatedAt.Format(time.RFC3339)},
	}
	var b strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&b, "%s\t%s\n", row[0], row[1])
	}
	_, err := fmt.Fprint(ios.Out, b.String())
	return err
}
