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
)

var (
	modelTypes = []string{"general", "llm"}
	useCases   = []string{"vision", "nlp", "llm", "speech", "other"}
)

func newCmdCreate(f *cmdutil.Factory) *cobra.Command {
	var (
		description string
		private     bool
		modelType   string
		useCase     string
		tags        []string
		exporter    *cmdutil.Exporter
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a repository",
		Long: `Create a repository in your account (the account behind your token).

On success a confirmation goes to stderr and stdout stays empty; with
--json the created resource object is written to stdout exactly as the
API returned it.

Creating repositories requires a token with the write scope.

Exit codes: 0 created, 1 API error (including a 409 name conflict and
403 missing scope), 2 usage error, 4 not authenticated.`,
		Example: `  # Create a public repository
  melange repo create whisper-tiny

  # Create a private LLM repository with metadata
  melange repo create phi-mini --private --model-type llm --description "Phi for mobile" --tag llm --tag mobile

  # Agent pattern: create and capture the full name
  melange repo create whisper-tiny --json --jq .full_name`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if strings.Contains(name, "/") {
				return cmdutil.FlagError{Err: fmt.Errorf(
					"invalid name %q: repositories are created in your own account; pass NAME without an account prefix", name)}
			}
			if !slices.Contains(modelTypes, modelType) {
				return cmdutil.FlagError{Err: fmt.Errorf(
					"invalid --model-type %q (expected one of: %s)", modelType, strings.Join(modelTypes, ", "))}
			}
			if useCase != "" && !slices.Contains(useCases, useCase) {
				return cmdutil.FlagError{Err: fmt.Errorf(
					"invalid --use-case %q (expected one of: %s)", useCase, strings.Join(useCases, ", "))}
			}

			g, _, err := genClient(f)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			body := gen.CreateRepoJSONRequestBody{
				Name:      name,
				ModelType: gen.CreateRepoRequestModelType(modelType),
			}
			if description != "" {
				body.Description = &description
			}
			if len(tags) > 0 {
				body.Tags = &tags
			}
			if private {
				body.IsPrivate = &private
			}
			if useCase != "" {
				uc := gen.CreateRepoRequestUseCase(useCase)
				body.UseCase = &uc
			}

			resp, err := g.CreateRepoWithResponse(ctx, body)
			if err != nil {
				return err
			}
			if err := api.ErrorFrom(resp.StatusCode(), resp.HTTPResponse.Header, resp.Body); err != nil {
				var apiErr *api.Error
				if errors.As(err, &apiErr) && apiErr.StatusCode == 403 {
					return fmt.Errorf(
						"%w\nCreating repositories requires a token with the write scope; "+
							"check `melange auth status` and create a new token if needed", err)
				}
				return err
			}
			repo := resp.JSON201
			raw := resp.Body
			if repo == nil {
				return fmt.Errorf("unexpected response creating repository %s (HTTP %d)", name, resp.StatusCode())
			}

			ios := f.IOStreams
			fmt.Fprintf(ios.ErrOut, "✓ Created repository %s\n", repo.FullName)
			if exporter != nil {
				return exporter.Write(ios, json.RawMessage(raw))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&description, "description", "", "Repository description")
	cmd.Flags().BoolVar(&private, "private", false, "Make the repository private")
	cmd.Flags().StringVar(&modelType, "model-type", "general",
		"Model type: {general|llm}")
	cmd.Flags().StringVar(&useCase, "use-case", "",
		"Use case: {vision|nlp|llm|speech|other}")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "Add a tag (repeatable)")
	cmdutil.AddJSONFlags(cmd, &exporter)

	return cmd
}
