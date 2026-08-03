package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/wait"
)

// Conversion polling budget. maxWaitSeconds is deliberately short: an agent
// should re-call get_conversion_status rather than hold one long request open.
const (
	maxWaitSeconds = 120
	pollInitial    = 2 * time.Second
	pollFactor     = 1.5
	pollCap        = 30 * time.Second
)

// Poll hooks: nil selects the real jitter/sleeper/clock in internal/wait.
// Tests inject deterministic implementations so no test ever sleeps.
// Mirrors the same seam in internal/cmd/model.
var (
	pollJitter func(time.Duration) time.Duration
	pollSleep  func(context.Context, time.Duration) error
	pollNow    func() time.Time
)

// registerModel registers the model read and write tools.
func registerModel(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_models",
		Description: "List a repository's models, newest first, with each model's key, " +
			"version, type, state, and whether it is the repository default. Model keys are " +
			"opaque — always take one from this listing instead of constructing it.",
		InputSchema: inputSchemaFor[listModelsArgs](withPageBounds),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
		},
	}, listModelsHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_model",
		Description: "Show one model: version, type, state, default flag, source, " +
			"terminal and download-ready flags, failure code, and timestamps. With " +
			"include_targets the result is instead the composite object " +
			`{"model": <model>, "targets": <target list>}, which adds the converted ` +
			"target artifacts and their opaque target_ids in a single call.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
		},
	}, getModelHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_conversion_status",
		Description: "Read a model's conversion status: state (converting, optimizing, " +
			"ready, or failed), pipeline stage, download readiness, and a failure code when " +
			"processing failed. Returns immediately by default; wait_seconds polls with " +
			"backoff and still returns the latest status when that budget runs out, so " +
			"prefer short waits and call again rather than blocking on one long request.",
		InputSchema: inputSchemaFor[conversionStatusArgs](withWaitBounds),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
		},
	}, getConversionStatusHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_default_model",
		Description: "Make one model the repository's default. Exactly one model per repository " +
			"is the default, so this also clears the previous one; repeating the call returns the " +
			"same result. Take model_key from list_models.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
		},
	}, setDefaultModelHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "import_model",
		Description: "Import a model from a public HuggingFace repository into an llm-type " +
			"repository (other repositories are rejected). Returns immediately with the new model " +
			"key while conversion continues in the background — follow with get_conversion_status " +
			"rather than assuming the model is ready. Each call starts a new import; the revision " +
			"is always the HuggingFace repository's current default-branch head.",
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  false,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   truePtr(),
		},
	}, importModelHandler(d))
}

// setDefaultModelArgs are the arguments of set_default_model.
type setDefaultModelArgs struct {
	Repo     string `json:"repo" jsonschema:"Repository in ACCOUNT/NAME form (example: zetic/whisper-tiny)."`
	ModelKey string `json:"model_key" jsonschema:"Opaque model key from list_models."`
}

// setDefaultModelHandler wraps PUT .../models/{key}/default.
func setDefaultModelHandler(d Deps) mcp.ToolHandlerFor[setDefaultModelArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in setDefaultModelArgs) (*mcp.CallToolResult, any, error) {
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}
		resp, err := g.SetDefaultModelWithResponse(ctx, account, name, in.ModelKey)
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return toolError(err), nil, nil
		}
		return rawResult(resp.Body), nil, nil
	}
}

// importModelArgs are the arguments of import_model.
type importModelArgs struct {
	Repo   string `json:"repo" jsonschema:"Target repository in ACCOUNT/NAME form; its model type must be llm."`
	HfRepo string `json:"hf_repo" jsonschema:"Public HuggingFace repository to import, as OWNER/NAME (example: meta-llama/Llama-3.2-1B); hf:// and URL prefixes are accepted."`
}

// importModelHandler wraps POST .../models/import. Each call carries a fresh
// Idempotency-Key, so the API transport may safely replay one logical import
// after a transient failure without starting a second one.
func importModelHandler(d Deps) mcp.ToolHandlerFor[importModelArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in importModelArgs) (*mcp.CallToolResult, any, error) {
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}
		resp, err := g.ImportModelWithResponse(ctx, account, name,
			&gen.ImportModelParams{IdempotencyKey: newIdempotencyKeyParam()},
			gen.ImportModelJSONRequestBody{HfRepo: in.HfRepo})
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return toolError(err), nil, nil
		}
		return rawResult(resp.Body), nil, nil
	}
}

// listModelsArgs are the arguments of list_models.
type listModelsArgs struct {
	Repo   string `json:"repo" jsonschema:"Repository in ACCOUNT/NAME form (example: zetic/whisper-tiny)."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Page size, 1-100; omit for 30."`
	Offset int    `json:"offset,omitempty" jsonschema:"Number of results to skip before this page."`
}

// listModelsHandler wraps GET /v1/repos/{account}/{repo}/models.
func listModelsHandler(d Deps) mcp.ToolHandlerFor[listModelsArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listModelsArgs) (*mcp.CallToolResult, any, error) {
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}
		limit := pageLimit(in.Limit)
		resp, err := g.ListModelsWithResponse(ctx, account, name,
			&gen.ListModelsParams{Limit: &limit, Offset: &in.Offset})
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return toolError(err), nil, nil
		}
		return rawResult(resp.Body), nil, nil
	}
}

// getModelArgs are the arguments of get_model.
type getModelArgs struct {
	Repo           string `json:"repo" jsonschema:"Repository in ACCOUNT/NAME form (example: zetic/whisper-tiny)."`
	ModelKey       string `json:"model_key" jsonschema:"Opaque model key from list_models."`
	IncludeTargets bool   `json:"include_targets,omitempty" jsonschema:"Also fetch the model's converted targets and return them alongside the model."`
}

// modelWithTargets is the composite get_model envelope. Both halves stay
// json.RawMessage so each API response survives byte-for-byte.
type modelWithTargets struct {
	Model   json.RawMessage `json:"model"`
	Targets json.RawMessage `json:"targets"`
}

// getModelHandler wraps GET /v1/repos/{account}/{repo}/models/{key}, adding
// the model's targets when the caller asks for them.
func getModelHandler(d Deps) mcp.ToolHandlerFor[getModelArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in getModelArgs) (*mcp.CallToolResult, any, error) {
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}
		resp, err := g.GetModelWithResponse(ctx, account, name, in.ModelKey)
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return toolError(err), nil, nil
		}
		if !in.IncludeTargets {
			return rawResult(resp.Body), nil, nil
		}

		targets, err := g.ListModelTargetsWithResponse(ctx, account, name, in.ModelKey)
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(targets.StatusCode(), targets.HTTPResponse, targets.Body); err != nil {
			return toolError(err), nil, nil
		}
		envelope, err := marshalEnvelope(modelWithTargets{Model: resp.Body, Targets: targets.Body})
		if err != nil {
			// Both halves are API JSON we already accepted; a failure here is a
			// programming fault, not something the caller can act on.
			return nil, nil, fmt.Errorf("building get_model envelope: %w", err)
		}
		return rawResult(envelope), nil, nil
	}
}

// conversionStatusArgs are the arguments of get_conversion_status.
type conversionStatusArgs struct {
	Repo        string `json:"repo" jsonschema:"Repository in ACCOUNT/NAME form (example: zetic/whisper-tiny)."`
	ModelKey    string `json:"model_key" jsonschema:"Opaque model key from list_models."`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"Seconds to poll for a terminal state, 0-120. 0 reads the status once and returns."`
}

// withWaitBounds caps the polling budget in the schema, so an over-long wait
// is refused before any request is made.
func withWaitBounds(props map[string]*jsonschema.Schema) {
	minWait, maxWait := 0.0, float64(maxWaitSeconds)
	props["wait_seconds"].Minimum = &minWait
	props["wait_seconds"].Maximum = &maxWait
}

// getConversionStatusHandler wraps GET .../models/{key}/status, optionally
// polling until the model reaches a terminal state. An exhausted budget is
// not an error: the latest status is returned so the caller can decide
// whether to poll again.
func getConversionStatusHandler(d Deps) mcp.ToolHandlerFor[conversionStatusArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in conversionStatusArgs) (*mcp.CallToolResult, any, error) {
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}

		// fetch reports the raw status body and whether the API considers the
		// state terminal — the same signal `melange model status --wait` uses.
		fetch := func(ctx context.Context) (body []byte, terminal bool, err error) {
			resp, err := g.GetModelStatusWithResponse(ctx, account, name, in.ModelKey)
			if err != nil {
				return nil, false, err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return nil, false, aerr
			}
			if resp.JSON200 == nil {
				return nil, false, fmt.Errorf(
					"unexpected response fetching model status (HTTP %d)", resp.StatusCode())
			}
			return resp.Body, resp.JSON200.Terminal, nil
		}

		if in.WaitSeconds <= 0 {
			body, _, err := fetch(ctx)
			if err != nil {
				return toolError(err), nil, nil
			}
			return rawResult(body), nil, nil
		}

		var latest []byte
		err = wait.Poll(ctx, wait.Options{
			Initial: pollInitial,
			Factor:  pollFactor,
			Cap:     pollCap,
			Timeout: time.Duration(in.WaitSeconds) * time.Second,
			Jitter:  pollJitter,
			Sleep:   pollSleep,
			Now:     pollNow,
		}, func(ctx context.Context) (bool, error) {
			body, terminal, err := fetch(ctx)
			if err != nil {
				return false, err
			}
			latest = body
			return terminal, nil
		})
		switch {
		case errors.Is(err, wait.ErrTimeout):
			if latest == nil {
				// The budget expired before any status came back.
				return toolError(fmt.Errorf(
					"no conversion status returned within %ds; call get_conversion_status again",
					in.WaitSeconds)), nil, nil
			}
		case err != nil:
			return toolError(err), nil, nil
		}
		return rawResult(latest), nil, nil
	}
}
