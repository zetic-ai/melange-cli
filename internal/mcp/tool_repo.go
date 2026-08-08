package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// registerRepo registers the repository read and write tools.
func registerRepo(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_repos",
		Title: "List repositories",
		Description: "List the repositories this token can see, newest first. " +
			"Start here to discover the ACCOUNT/NAME identifier the model tools take; " +
			"'search' filters server-side, and 'offset' walks further pages.",
		InputSchema:  inputSchemaFor[listReposArgs](withPageBounds),
		OutputSchema: outputSchema("list_repos"),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, listReposHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_repo",
		Title: "Get repository",
		Description: "Show one repository: visibility, model type, use case, tags, " +
			"description, and timestamps. The model type decides what may be uploaded " +
			"or imported into it.",
		OutputSchema: outputSchema("get_repo"),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, getRepoHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_repo",
		Title: "Create repository",
		Description: "Create a repository in the account behind the token — pass NAME alone, " +
			"never ACCOUNT/NAME. model_type fixes what the repository will accept and cannot be " +
			"changed later: only an llm repository accepts import_model.",
		InputSchema:  inputSchemaFor[createRepoArgs](withCreateRepoVocabulary),
		OutputSchema: outputSchema("create_repo"),
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  false,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, createRepoHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_repo",
		Title: "Update repository",
		Description: "Update a repository's metadata. Only the arguments you pass change: " +
			"tags replace the entire existing tag set, an empty description clears it, and " +
			"changing visibility is restricted to the repository owner.",
		InputSchema:  inputSchemaFor[updateRepoArgs](withUpdateRepoVocabulary),
		OutputSchema: outputSchema("update_repo"),
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, updateRepoHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "delete_repo",
		Title: "Delete repository",
		Description: "Permanently delete a repository and every model in it; this cannot be " +
			"undone and is restricted to the repository owner. Ask the user for explicit consent " +
			"first, then repeat the exact ACCOUNT/NAME in 'confirm' — without it nothing is deleted.",
		OutputSchema: outputSchema("delete_repo"),
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  true,
			DestructiveHint: truePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, deleteRepoHandler(d))
}

// createRepoArgs are the arguments of create_repo. They mirror the flags of
// `melange repo create`, typed against gen.CreateRepoRequest.
type createRepoArgs struct {
	Name        string   `json:"name" jsonschema:"Repository name WITHOUT an account prefix; it is created in the account behind the token."`
	Private     bool     `json:"private,omitempty" jsonschema:"Create the repository private; omit for public."`
	ModelType   string   `json:"model_type,omitempty" jsonschema:"What the repository accepts, fixed at creation; omit for general. Only llm repositories accept import_model."`
	UseCase     string   `json:"use_case,omitempty" jsonschema:"Primary use case of the models this repository will hold."`
	Tags        []string `json:"tags,omitempty" jsonschema:"Tags describing the repository."`
	Description string   `json:"description,omitempty" jsonschema:"Short description of the repository."`
}

// withCreateRepoVocabulary advertises the accepted model types and use cases,
// taken from the generated constants, so an unknown value is refused by the
// schema instead of reaching the API as a 422.
func withCreateRepoVocabulary(props map[string]*jsonschema.Schema) {
	props["model_type"].Enum = enumValues(
		gen.CreateRepoRequestModelTypeGeneral,
		gen.CreateRepoRequestModelTypeLlm,
	)
	props["use_case"].Enum = enumValues(
		gen.CreateRepoRequestUseCaseVision,
		gen.CreateRepoRequestUseCaseNlp,
		gen.CreateRepoRequestUseCaseLlm,
		gen.CreateRepoRequestUseCaseSpeech,
		gen.CreateRepoRequestUseCaseOther,
	)
}

// createRepoHandler wraps POST /v1/repos.
func createRepoHandler(d Deps) mcp.ToolHandlerFor[createRepoArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in createRepoArgs) (*mcp.CallToolResult, any, error) {
		if refusal := d.requireScope(ctx, scopeWrite); refusal != nil {
			return refusal, nil, nil
		}
		if strings.Contains(in.Name, "/") {
			return d.toolError(fmt.Errorf(
				"invalid name %q: repositories are always created in the account behind the token; "+
					"pass NAME without an ACCOUNT/ prefix", in.Name)), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}

		// The general default lives here rather than in the schema: the SDK
		// panics while filling schema defaults into a request that sends a
		// literal "arguments": null.
		modelType := in.ModelType
		if modelType == "" {
			modelType = string(gen.CreateRepoRequestModelTypeGeneral)
		}
		body := gen.CreateRepoJSONRequestBody{
			Name:      in.Name,
			ModelType: gen.CreateRepoRequestModelType(modelType),
		}
		if in.Description != "" {
			body.Description = &in.Description
		}
		if len(in.Tags) > 0 {
			body.Tags = &in.Tags
		}
		if in.Private {
			body.IsPrivate = &in.Private
		}
		if in.UseCase != "" {
			useCase := gen.CreateRepoRequestUseCase(in.UseCase)
			body.UseCase = &useCase
		}

		resp, err := g.CreateRepoWithResponse(ctx, body)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		return rawResult(resp.Body)
	}
}

// updateRepoArgs are the arguments of update_repo. Description and Private are
// pointers because the PATCH applies exactly the fields present in the body:
// an absent argument must leave the field alone, while an explicitly empty
// description clears it.
type updateRepoArgs struct {
	Repo        string   `json:"repo" jsonschema:"Repository in ACCOUNT/NAME form (example: zetic/whisper-tiny)."`
	Description *string  `json:"description,omitempty" jsonschema:"Replacement description; an empty string clears it. Omit to leave it unchanged."`
	Private     *bool    `json:"private,omitempty" jsonschema:"New visibility: true makes the repository private, false public. Owner only. Omit to leave it unchanged."`
	UseCase     string   `json:"use_case,omitempty" jsonschema:"Replacement use case. Omit to leave it unchanged."`
	Tags        []string `json:"tags,omitempty" jsonschema:"Replacement tag set — it replaces every existing tag, so list every tag the repository should end up with. Omit to leave them unchanged."`
}

// withUpdateRepoVocabulary advertises the accepted use cases of the PATCH body.
func withUpdateRepoVocabulary(props map[string]*jsonschema.Schema) {
	props["use_case"].Enum = enumValues(
		gen.UpdateRepoRequestUseCaseVision,
		gen.UpdateRepoRequestUseCaseNlp,
		gen.UpdateRepoRequestUseCaseLlm,
		gen.UpdateRepoRequestUseCaseSpeech,
		gen.UpdateRepoRequestUseCaseOther,
	)
}

// updateRepoHandler wraps PATCH /v1/repos/{account}/{repo}.
func updateRepoHandler(d Deps) mcp.ToolHandlerFor[updateRepoArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in updateRepoArgs) (*mcp.CallToolResult, any, error) {
		if refusal := d.requireScope(ctx, scopeWrite); refusal != nil {
			return refusal, nil, nil
		}
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return d.toolError(err), nil, nil
		}

		body := gen.UpdateRepoJSONRequestBody{Description: in.Description, IsPrivate: in.Private}
		if in.UseCase != "" {
			useCase := gen.UpdateRepoRequestUseCase(in.UseCase)
			body.UseCase = &useCase
		}
		// A nil slice is an omitted argument; an empty one clears the tags.
		if in.Tags != nil {
			body.Tags = &in.Tags
		}
		if body == (gen.UpdateRepoJSONRequestBody{}) {
			return d.toolError(fmt.Errorf(
				"nothing to update for %s: pass at least one of description, private, use_case, or tags",
				in.Repo)), nil, nil
		}

		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		// The PATCH carries exactly the provided fields, so replaying it cannot
		// apply the change twice — the same marking `melange repo edit` uses.
		resp, err := g.UpdateRepoWithResponse(api.WithReplaySafe(ctx), account, name, body)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		return rawResult(resp.Body)
	}
}

// deleteRepoArgs are the arguments of delete_repo. Confirm is deliberately
// optional in the schema: an agent that omits it must get the consent
// instructions below, not a bare schema-validation failure.
type deleteRepoArgs struct {
	Repo    string `json:"repo" jsonschema:"Repository to delete, in ACCOUNT/NAME form (example: zetic/whisper-tiny)."`
	Confirm string `json:"confirm,omitempty" jsonschema:"Repeat the repo argument exactly to confirm the deletion. Only send it after the user has explicitly consented to deleting this repository."`
}

// deletedRepo is the delete_repo success payload. The API answers 204 with no
// body, and a tool result must still carry something the caller can act on.
type deletedRepo struct {
	Deleted bool   `json:"deleted"`
	Repo    string `json:"repo"`
}

// deleteRepoHandler wraps DELETE /v1/repos/{account}/{repo} behind a
// confirmation gate that must pass before any request is made.
func deleteRepoHandler(d Deps) mcp.ToolHandlerFor[deleteRepoArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in deleteRepoArgs) (*mcp.CallToolResult, any, error) {
		if refusal := d.requireScope(ctx, scopeWrite); refusal != nil {
			return refusal, nil, nil
		}
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if in.Confirm != in.Repo {
			detail := "the confirm argument is missing"
			if in.Confirm != "" {
				detail = fmt.Sprintf("confirm %q does not match repo %q", in.Confirm, in.Repo)
			}
			return d.toolError(fmt.Errorf(
				"delete_repo refused: %s. Nothing was deleted. Deleting %s destroys every model "+
					"in it and cannot be undone: obtain explicit consent from the user first, then "+
					"call delete_repo again with confirm: %q — the exact ACCOUNT/NAME",
				detail, in.Repo, in.Repo)), nil, nil
		}

		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		resp, err := g.DeleteRepoWithResponse(ctx, account, name)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		result, err := marshalEnvelope(deletedRepo{Deleted: true, Repo: in.Repo})
		if err != nil {
			// A two-field struct that fails to marshal is a programming fault,
			// not something the caller can act on.
			return nil, nil, fmt.Errorf("building delete_repo result: %w", err)
		}
		return rawResult(result)
	}
}

// listReposArgs are the arguments of list_repos.
type listReposArgs struct {
	Search string `json:"search,omitempty" jsonschema:"Filter repositories by name, description, or tags."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Page size, 1-100; omit for 30."`
	Offset int    `json:"offset,omitempty" jsonschema:"Number of results to skip before this page."`
}

// listReposHandler wraps GET /v1/repos as a raw-passthrough tool.
func listReposHandler(d Deps) mcp.ToolHandlerFor[listReposArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listReposArgs) (*mcp.CallToolResult, any, error) {
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		limit := pageLimit(in.Limit)
		params := &gen.ListReposParams{Limit: &limit, Offset: &in.Offset}
		if in.Search != "" {
			params.Search = &in.Search
		}
		resp, err := g.ListReposWithResponse(ctx, params)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		return rawResult(resp.Body)
	}
}

// getRepoArgs are the arguments of get_repo.
type getRepoArgs struct {
	Repo string `json:"repo" jsonschema:"Repository in ACCOUNT/NAME form (example: zetic/whisper-tiny)."`
}

// getRepoHandler wraps GET /v1/repos/{account}/{repo} as a raw-passthrough tool.
func getRepoHandler(d Deps) mcp.ToolHandlerFor[getRepoArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in getRepoArgs) (*mcp.CallToolResult, any, error) {
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		resp, err := g.GetRepoWithResponse(ctx, account, name)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		return rawResult(resp.Body)
	}
}
