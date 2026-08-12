package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// Gen returns a generated OpenAPI client bound to this client's host that
// rides the same transport chain (auth -> retry -> debug), so every generated
// call carries the Bearer token, User-Agent, and retry behavior.
//
// Convert non-2xx generated responses with GenError:
//
//	if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
//		return err
//	}
func (c *Client) Gen() (*gen.ClientWithResponses, error) {
	return gen.NewClientWithResponses(c.baseURL.String(), gen.WithHTTPClient(c.http))
}

// GetMe fetches the identity behind the client's token via the generated
// client. Shared by auth commands and account-name resolution.
func (c *Client) GetMe(ctx context.Context) (*gen.MeResponse, error) {
	g, err := c.Gen()
	if err != nil {
		return nil, err
	}
	resp, err := g.GetMeWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if err := GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
		return nil, err
	}
	me := resp.JSON200
	if me == nil {
		// 2xx without the json content type (e.g. a proxy quirk): decode the
		// body directly rather than failing on the header.
		me = &gen.MeResponse{}
		if err := json.Unmarshal(resp.Body, me); err != nil {
			return nil, fmt.Errorf("decoding /v1/me response: %w", err)
		}
	}
	return me, nil
}

// GetBillingPlan fetches the effective billing plan for the client's token
// via the generated client. Shared by the plan command and auth status.
func (c *Client) GetBillingPlan(ctx context.Context) (*gen.BillingPlanResponse, error) {
	g, err := c.Gen()
	if err != nil {
		return nil, err
	}
	resp, err := g.GetBillingPlanWithResponse(ctx)
	if err != nil {
		return nil, err
	}
	if err := GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
		return nil, err
	}
	plan := resp.JSON200
	if plan == nil {
		// 2xx without the json content type (e.g. a proxy quirk): decode the
		// body directly rather than failing on the header.
		plan = &gen.BillingPlanResponse{}
		if err := json.Unmarshal(resp.Body, plan); err != nil {
			return nil, fmt.Errorf("decoding /v1/billing/plan response: %w", err)
		}
	}
	return plan, nil
}

// ztcPackageRequest is the body for the ztc-package (non-llama.cpp) LLM import:
// a public HuggingFace repo id the server composes prefill+decode from into an
// encrypted .ztc, rather than the default llama.cpp GGUF path. The field name
// mirrors the web UI's staff-only endpoint contract ({"uri": "<hf_repo>"}).
type ztcPackageRequest struct {
	URI string `json:"uri"`
}

// ImportModelZtcPackage triggers the non-llama.cpp ZTC package conversion path
// for a HuggingFace repo, mirroring the web UI's staff-only endpoint. It rides
// this client's transport chain (auth -> retry -> debug) exactly like the
// generated ImportModel call, and carries an Idempotency-Key so the retry
// transport may safely replay it. It returns the decoded response, the raw
// response bytes (for --json/--jq output), and a GenError-converted error.
//
// BACKEND: the public route is assumed to be
// POST /v1/repos/{account_name}/{repo_name}/models/ztc-package with body
// {"uri": "<hf_repo>"}, aligned to the existing import route. The path lives
// here alone, so adjust this one line if the backend exposes a different route.
func (c *Client) ImportModelZtcPackage(ctx context.Context, accountName, repoName, hfRepo, idempotencyKey string) (*gen.ImportModelResponse, []byte, error) {
	reqBody, err := json.Marshal(ztcPackageRequest{URI: hfRepo})
	if err != nil {
		return nil, nil, err
	}
	path := fmt.Sprintf("/v1/repos/%s/%s/models/ztc-package", accountName, repoName)
	resp, err := c.Do(ctx, http.MethodPost, path, bytes.NewReader(reqBody), map[string]string{
		"Content-Type":    "application/json",
		"Idempotency-Key": idempotencyKey,
	})
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading ztc-package response: %w", err)
	}
	if err := ErrorFrom(resp.StatusCode, resp.Header, raw); err != nil {
		return nil, nil, err
	}
	var imported gen.ImportModelResponse
	if err := json.Unmarshal(raw, &imported); err != nil {
		return nil, nil, fmt.Errorf("decoding ztc-package response: %w", err)
	}
	return &imported, raw, nil
}

// GenError converts a generated-client response into an error: nil for 2xx,
// otherwise an *Error via ErrorFrom. It is THE way to surface non-2xx results
// from generated responses; it tolerates a nil HTTPResponse.
func GenError(status int, httpResp *http.Response, body []byte) error {
	var header http.Header
	if httpResp != nil {
		header = httpResp.Header
	}
	return ErrorFrom(status, header, body)
}
