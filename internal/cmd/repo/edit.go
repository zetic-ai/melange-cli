package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func newCmdEdit(f *cmdutil.Factory) *cobra.Command {
	var (
		description string
		visibility  string
		useCase     string
		tags        []string
		exporter    *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "edit <[account/]name>",
		Short: "Edit a repository",
		Long: `Edit a repository's metadata: description, visibility, use case, and
tags.

Only the fields you pass are changed (the PATCH body carries exactly the
provided fields): --description "" clears the description, and --tag
replaces the complete tag set atomically — repeat it once per tag you
want the repository to end up with.

Changing --visibility is restricted to the repository owner server-side;
other members get the server's 403 message.

On success a confirmation goes to stderr and stdout stays empty; with
--json, API fields and order are preserved and output ends with exactly one
trailing newline.

Exit codes: 0 updated, 1 API error (including 403 permission and 404
not found), 2 usage error, 4 not authenticated.`,
		Example: `  # Update the description
  melange repo edit zetic/whisper-tiny --description "Tiny Whisper for on-device ASR"

  # Make a repository public and replace its tags
  melange repo edit zetic/whisper-tiny --visibility public --tag asr --tag tiny

  # Agent pattern: edit and confirm the resulting visibility
  melange repo edit zetic/whisper-tiny --visibility private --json --jq .is_private`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			account, name, err := splitRepoArg(args[0])
			if err != nil {
				return err
			}

			fl := cmd.Flags()
			body := gen.UpdateRepoJSONRequestBody{}
			edited := false
			if fl.Changed("description") {
				body.Description = &description
				edited = true
			}
			if fl.Changed("visibility") {
				if visibility != "public" && visibility != "private" {
					return cmdutil.FlagError{Err: fmt.Errorf(
						"invalid --visibility %q (expected public or private)", visibility)}
				}
				isPrivate := visibility == "private"
				body.IsPrivate = &isPrivate
				edited = true
			}
			if fl.Changed("use-case") {
				if !slices.Contains(useCases, useCase) {
					return cmdutil.FlagError{Err: fmt.Errorf(
						"invalid --use-case %q (expected one of: %s)", useCase, strings.Join(useCases, ", "))}
				}
				uc := gen.UpdateRepoRequestUseCase(useCase)
				body.UseCase = &uc
				edited = true
			}
			if fl.Changed("tag") {
				body.Tags = &tags
				edited = true
			}
			if !edited {
				return cmdutil.FlagError{Err: errors.New(
					"nothing to edit; pass at least one of --description, --visibility, --use-case, or --tag")}
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

			resp, err := g.UpdateRepoWithResponse(api.WithReplaySafe(ctx), account, name, body)
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			repo := resp.JSON200
			if repo == nil {
				return fmt.Errorf("unexpected response updating repository %s/%s (HTTP %d)",
					account, name, resp.StatusCode())
			}

			ios := f.IOStreams
			fmt.Fprintf(ios.ErrOut, "✓ Updated repository %s\n",
				text.SanitizeTerminalInline(repo.FullName))
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(resp.Body))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&description, "description", "", "New description (\"\" clears it)")
	cmd.Flags().StringVar(&visibility, "visibility", "", "Visibility: {public|private} (owner only)")
	cmd.Flags().StringVar(&useCase, "use-case", "", "Use case: {vision|nlp|llm|speech|other}")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "Replacement tag (repeatable; replaces the whole tag set)")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}
