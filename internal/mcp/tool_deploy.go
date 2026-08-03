package mcp

import (
	"context"
	"errors"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// registerDeploy registers the deployment read tool.
func registerDeploy(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_deployment_info",
		Description: "Answer deployment questions in one of two modes. Called with no " +
			"arguments it returns the catalog: every supported SDK language, every " +
			"inference mode, and the defaults. Called with repo and model_key it returns " +
			"that model version's deployment guide — SDK, install steps, and inference " +
			"code — optionally narrowed by language and inference_mode. Guides carry the " +
			"literal YOUR_PERSONAL_KEY placeholder; no real credential is ever emitted, " +
			"so tell the user to substitute their own key.",
		InputSchema:  inputSchemaFor[deploymentInfoArgs](withDeploymentEnums),
		OutputSchema: outputSchema("get_deployment_info"),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
		},
	}, getDeploymentInfoHandler(d))
}

// deploymentInfoArgs are the arguments of get_deployment_info. Every field is
// optional: model_key is what selects the guide over the options catalog.
type deploymentInfoArgs struct {
	Repo          string `json:"repo,omitempty" jsonschema:"Repository in ACCOUNT/NAME form (example: zetic/whisper-tiny). Required with model_key."`
	ModelKey      string `json:"model_key,omitempty" jsonschema:"Opaque model key from list_models. Omit to list the supported deployment options instead of a guide."`
	Language      string `json:"language,omitempty" jsonschema:"SDK language for the guide; omit for the platform default."`
	InferenceMode string `json:"inference_mode,omitempty" jsonschema:"Inference mode for the guide; omit for the platform default."`
}

// withDeploymentEnums advertises the exact selectors the guide endpoint
// accepts, so an unsupported language (React Native, say) is refused by the
// schema instead of costing a round trip.
func withDeploymentEnums(props map[string]*jsonschema.Schema) {
	props["language"].Enum = enumValues(
		gen.GetDeploymentGuideParamsLanguageAndroidKotlin,
		gen.GetDeploymentGuideParamsLanguageAndroidJava,
		gen.GetDeploymentGuideParamsLanguageIosSwift,
		gen.GetDeploymentGuideParamsLanguageFlutter,
	)
	props["inference_mode"].Enum = enumValues(
		gen.GetDeploymentGuideParamsInferenceModeAuto,
		gen.GetDeploymentGuideParamsInferenceModeSpeed,
		gen.GetDeploymentGuideParamsInferenceModeAccuracy,
	)
}

// getDeploymentInfoHandler wraps GET /v1/deployment/options and
// GET /v1/repos/{account}/{repo}/models/{key}/deployment-guide. model_key
// picks between them: the options catalog is account-independent, so asking
// for it while naming a model would silently drop the model.
func getDeploymentInfoHandler(d Deps) mcp.ToolHandlerFor[deploymentInfoArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in deploymentInfoArgs) (*mcp.CallToolResult, any, error) {
		if in.ModelKey == "" {
			if in.Repo != "" || in.Language != "" || in.InferenceMode != "" {
				return toolError(errors.New(
					"model_key is required to render a deployment guide; pass repo and " +
						"model_key, or call get_deployment_info with no arguments to list " +
						"the supported languages and inference modes")), nil, nil
			}
			g, err := d.Clients.Client(ctx)
			if err != nil {
				return toolError(err), nil, nil
			}
			return deploymentOptions(ctx, g)
		}

		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}
		params := &gen.GetDeploymentGuideParams{}
		if in.Language != "" {
			language := gen.GetDeploymentGuideParamsLanguage(in.Language)
			params.Language = &language
		}
		if in.InferenceMode != "" {
			mode := gen.GetDeploymentGuideParamsInferenceMode(in.InferenceMode)
			params.InferenceMode = &mode
		}
		resp, err := g.GetDeploymentGuideWithResponse(ctx, account, name, in.ModelKey, params)
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return toolError(err), nil, nil
		}
		return rawResult(resp.Body), nil, nil
	}
}

// deploymentOptions reads the platform-wide selector catalog.
func deploymentOptions(ctx context.Context, g *gen.ClientWithResponses) (*mcp.CallToolResult, any, error) {
	resp, err := g.GetDeploymentOptionsWithResponse(ctx)
	if err != nil {
		return toolError(err), nil, nil
	}
	if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
		return toolError(err), nil, nil
	}
	return rawResult(resp.Body), nil, nil
}
