package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// registerRepo registers the repository read tools.
func registerRepo(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_repos",
		Description: "List the repositories this token can see, newest first. " +
			"Start here to discover the ACCOUNT/NAME identifier the model tools take; " +
			"'search' filters server-side, and 'offset' walks further pages.",
		InputSchema: inputSchemaFor[listReposArgs](withPageBounds),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
		},
	}, listReposHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_repo",
		Description: "Show one repository: visibility, model type, use case, tags, " +
			"description, and timestamps. The model type decides what may be uploaded " +
			"or imported into it.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
		},
	}, getRepoHandler(d))
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
			return toolError(err), nil, nil
		}
		limit := pageLimit(in.Limit)
		params := &gen.ListReposParams{Limit: &limit, Offset: &in.Offset}
		if in.Search != "" {
			params.Search = &in.Search
		}
		resp, err := g.ListReposWithResponse(ctx, params)
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return toolError(err), nil, nil
		}
		return rawResult(resp.Body), nil, nil
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
			return toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}
		resp, err := g.GetRepoWithResponse(ctx, account, name)
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return toolError(err), nil, nil
		}
		return rawResult(resp.Body), nil, nil
	}
}
