package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// registerLibrary registers the public model library read tools.
func registerLibrary(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "search_library",
		Description: "Search the public model library: models already converted and " +
			"benchmarked for on-device use. 'search' matches name or full_name ignoring " +
			"case and separators, 'task' filters by use case, and 'provider' takes an " +
			"exact provider name. With include_providers the result is instead the " +
			`composite object {"models": <page>, "providers": <provider list>}, which is ` +
			"how to learn the exact provider names the filter expects. Each entry's " +
			"full_name is a repository coordinate usable directly with get_model_report " +
			"and get_deployment_info — never import a library model just to read its " +
			"public benchmarks.",
		InputSchema:  inputSchemaFor[searchLibraryArgs](withLibraryFilters),
		OutputSchema: outputSchema("search_library"),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, searchLibraryHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_library_model",
		Description: "Show one public library model: full name, provider, use-case task, " +
			"model type, tags, description, and the complete readme. Take the ACCOUNT/NAME " +
			"coordinate from search_library, then list its converted model keys with " +
			"list_models.",
		OutputSchema: outputSchema("get_library_model"),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, getLibraryModelHandler(d))
}

// searchLibraryArgs are the arguments of search_library.
type searchLibraryArgs struct {
	Search           string   `json:"search,omitempty" jsonschema:"Case- and separator-insensitive substring match on name or full_name."`
	Task             []string `json:"task,omitempty" jsonschema:"Use-case filter; a model matching ANY listed task is included."`
	Provider         string   `json:"provider,omitempty" jsonschema:"Exact provider (company) name; use include_providers to discover the valid names."`
	Limit            int      `json:"limit,omitempty" jsonschema:"Page size, 1-100; omit for 30."`
	Offset           int      `json:"offset,omitempty" jsonschema:"Number of results to skip before this page."`
	IncludeProviders bool     `json:"include_providers,omitempty" jsonschema:"Also fetch the provider list and return it alongside the models."`
}

// withLibraryFilters bounds the page and advertises the task vocabulary, so
// an unknown task never reaches the API as a filter that silently matches
// nothing.
func withLibraryFilters(props map[string]*jsonschema.Schema) {
	withPageBounds(props)
	props["task"].Items.Enum = enumValues(
		gen.ListLibraryModelsParamsTaskVision,
		gen.ListLibraryModelsParamsTaskLlm,
		gen.ListLibraryModelsParamsTaskNlp,
		gen.ListLibraryModelsParamsTaskSpeech,
		gen.ListLibraryModelsParamsTaskOther,
	)
}

// libraryWithProviders is the composite search_library envelope. Both halves
// stay json.RawMessage so each API response survives byte-for-byte.
type libraryWithProviders struct {
	Models    json.RawMessage `json:"models"`
	Providers json.RawMessage `json:"providers"`
}

// searchLibraryHandler wraps GET /v1/library/models, adding the provider list
// when the caller asks for it.
func searchLibraryHandler(d Deps) mcp.ToolHandlerFor[searchLibraryArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in searchLibraryArgs) (*mcp.CallToolResult, any, error) {
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		limit := pageLimit(in.Limit)
		params := &gen.ListLibraryModelsParams{Limit: &limit, Offset: &in.Offset}
		if in.Search != "" {
			params.Search = &in.Search
		}
		if in.Provider != "" {
			params.Provider = &in.Provider
		}
		if len(in.Task) > 0 {
			tasks := make([]gen.ListLibraryModelsParamsTask, len(in.Task))
			for i, t := range in.Task {
				tasks[i] = gen.ListLibraryModelsParamsTask(t)
			}
			params.Task = &tasks
		}
		resp, err := g.ListLibraryModelsWithResponse(ctx, params)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		if !in.IncludeProviders {
			return rawResult(resp.Body)
		}

		providers, err := g.ListLibraryProvidersWithResponse(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(providers.StatusCode(), providers.HTTPResponse, providers.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		envelope, err := marshalEnvelope(libraryWithProviders{Models: resp.Body, Providers: providers.Body})
		if err != nil {
			// Both halves are API JSON we already accepted; a failure here is a
			// programming fault, not something the caller can act on.
			return nil, nil, fmt.Errorf("building search_library envelope: %w", err)
		}
		return rawResult(envelope)
	}
}

// getLibraryModelArgs are the arguments of get_library_model.
type getLibraryModelArgs struct {
	LibraryModel string `json:"library_model" jsonschema:"Library model in ACCOUNT/NAME form — the full_name from search_library (example: zetic/whisper-tiny)."`
}

// getLibraryModelHandler wraps GET /v1/library/models/{account}/{name}.
func getLibraryModelHandler(d Deps) mcp.ToolHandlerFor[getLibraryModelArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in getLibraryModelArgs) (*mcp.CallToolResult, any, error) {
		account, name, err := splitLibraryModel(in.LibraryModel)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		resp, err := g.GetLibraryModelWithResponse(ctx, account, name)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		return rawResult(resp.Body)
	}
}

// splitLibraryModel parses a library entry's ACCOUNT/NAME coordinate. The
// form is the same as splitRepo's, but the remedy is not: library models are
// public entries that need not appear in the caller's own repository list.
func splitLibraryModel(s string) (account, name string, err error) {
	account, name, err = splitRepo(s)
	if err != nil {
		return "", "", fmt.Errorf(
			"invalid library_model %q: expected ACCOUNT/NAME (for example zetic/whisper-tiny); "+
				"call search_library to discover library models", s)
	}
	return account, name, nil
}
