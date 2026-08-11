package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// registerDeploy registers the deployment read tool.
func registerDeploy(s *mcp.Server, d Deps) {
	tool := &mcp.Tool{
		Name:  "get_deployment_info",
		Title: "Get deployment info",
		Description: "Answer deployment questions in one of two modes. Called with no " +
			"arguments it returns the catalog: every supported SDK language, every " +
			"inference mode, and the defaults. Called with repo and model_key it returns " +
			"that model version's deployment guide — SDK, install steps, and inference " +
			"code — optionally narrowed by language and inference_mode. Guides carry the " +
			"literal YOUR_PERSONAL_KEY placeholder; no real credential is ever emitted, " +
			"so tell the user to substitute their own key.",
		OutputSchema: outputSchema("get_deployment_info"),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}
	if d.Edition.IsQualcomm() {
		tool.InputSchema = inputSchemaFor[qualcommDeploymentInfoArgs](withQualcommDeploymentEnums)
		mcp.AddTool(s, tool, getQualcommDeploymentInfoHandler(d))
		return
	}
	tool.InputSchema = inputSchemaFor[deploymentInfoArgs](withDeploymentEnums)
	mcp.AddTool(s, tool, getDeploymentInfoHandler(d))
}

// deploymentInfoArgs are the arguments of get_deployment_info. Every field is
// optional: model_key is what selects the guide over the options catalog.
type deploymentInfoArgs struct {
	Repo          string `json:"repo,omitempty" jsonschema:"Repository in ACCOUNT/NAME form (example: zetic/whisper-tiny). Required with model_key."`
	ModelKey      string `json:"model_key,omitempty" jsonschema:"Opaque model key from list_models. Omit to list the supported deployment options instead of a guide."`
	Language      string `json:"language,omitempty" jsonschema:"SDK language for the guide; omit for the platform default."`
	InferenceMode string `json:"inference_mode,omitempty" jsonschema:"Inference mode for the guide; omit for the platform default."`
}

// qualcommDeploymentInfoArgs has its own schema identity so the MCP schema
// cache can safely hold the narrowed enum alongside the standard catalog.
type qualcommDeploymentInfoArgs deploymentInfoArgs

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
	withInferenceModeEnum(props)
}

func withQualcommDeploymentEnums(props map[string]*jsonschema.Schema) {
	props["language"].Enum = enumValues(
		gen.GetDeploymentGuideParamsLanguageAndroidKotlin,
		gen.GetDeploymentGuideParamsLanguageAndroidJava,
		gen.GetDeploymentGuideParamsLanguageFlutter,
	)
	withInferenceModeEnum(props)
}

func withInferenceModeEnum(props map[string]*jsonschema.Schema) {
	props["inference_mode"].Enum = enumValues(
		gen.GetDeploymentGuideParamsInferenceModeAuto,
		gen.GetDeploymentGuideParamsInferenceModeSpeed,
		gen.GetDeploymentGuideParamsInferenceModeAccuracy,
	)
}

func getQualcommDeploymentInfoHandler(d Deps) mcp.ToolHandlerFor[qualcommDeploymentInfoArgs, any] {
	handler := getDeploymentInfoHandler(d)
	return func(ctx context.Context, req *mcp.CallToolRequest, in qualcommDeploymentInfoArgs) (*mcp.CallToolResult, any, error) {
		return handler(ctx, req, deploymentInfoArgs(in))
	}
}

// getDeploymentInfoHandler wraps GET /v1/deployment/options and
// GET /v1/repos/{account}/{repo}/models/{key}/deployment-guide. model_key
// picks between them: the options catalog is account-independent, so asking
// for it while naming a model would silently drop the model.
func getDeploymentInfoHandler(d Deps) mcp.ToolHandlerFor[deploymentInfoArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in deploymentInfoArgs) (*mcp.CallToolResult, any, error) {
		if in.ModelKey == "" {
			if in.Repo != "" || in.Language != "" || in.InferenceMode != "" {
				return d.toolError(errors.New(
					"model_key is required to render a deployment guide; pass repo and " +
						"model_key, or call get_deployment_info with no arguments to list " +
						"the supported languages and inference modes")), nil, nil
			}
			g, err := d.Clients.Client(ctx)
			if err != nil {
				return d.toolError(err), nil, nil
			}
			return deploymentOptions(ctx, d, g)
		}

		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		params := &gen.GetDeploymentGuideParams{}
		if in.Language != "" {
			if d.Edition.IsQualcomm() && !d.Edition.AllowsDeploymentLanguage(in.Language) {
				return d.toolError(fmt.Errorf("unsupported deployment language %q for %s", in.Language, d.Edition.ProgramName())), nil, nil
			}
			language := gen.GetDeploymentGuideParamsLanguage(in.Language)
			params.Language = &language
		}
		if in.InferenceMode != "" {
			mode := gen.GetDeploymentGuideParamsInferenceMode(in.InferenceMode)
			params.InferenceMode = &mode
		}
		resp, err := g.GetDeploymentGuideWithResponse(ctx, account, name, in.ModelKey, params)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		if d.Edition.IsQualcomm() && resp.JSON200 != nil && !d.Edition.AllowsDeploymentLanguage(string(resp.JSON200.Language)) {
			return d.toolError(fmt.Errorf("deployment guide returned unsupported language %q for %s", resp.JSON200.Language, d.Edition.ProgramName())), nil, nil
		}
		return rawResult(resp.Body)
	}
}

// deploymentOptions reads the platform-wide selector catalog.
func deploymentOptions(ctx context.Context, d Deps, g *gen.ClientWithResponses) (*mcp.CallToolResult, any, error) {
	resp, err := g.GetDeploymentOptionsWithResponse(ctx)
	if err != nil {
		return d.toolError(err), nil, nil
	}
	if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
		return d.toolError(err), nil, nil
	}
	body, err := d.Edition.FilterDeploymentOptions(resp.Body)
	if err != nil {
		return d.toolError(err), nil, nil
	}
	return rawResult(body)
}
