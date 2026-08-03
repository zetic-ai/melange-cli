package mcp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// The three benchmark report shapes a model may carry, mirroring the
// `melange report view --type` values.
const (
	reportGeneral = "general"
	reportLLM     = "llm"
	reportPackage = "package"
)

// registerReport registers the benchmark report read tool.
func registerReport(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_model_report",
		Description: "Read one of a model's on-device benchmark reports. report_type " +
			"selects the shape: 'general' is per-device latency, SNR, and memory " +
			"measurements; 'llm' is tokens per second per quantization plus accuracy per " +
			"dataset; 'package' is a mode-by-metric summary. A model only carries the " +
			"report types its model type produces, so a not-found error means that shape " +
			"does not exist for it — try another report_type. Public library models can " +
			"be benchmarked this way without importing them.",
		InputSchema: inputSchemaFor[modelReportArgs](withReportTypeEnum),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
		},
	}, getModelReportHandler(d))
}

// modelReportArgs are the arguments of get_model_report.
type modelReportArgs struct {
	Repo       string `json:"repo" jsonschema:"Repository in ACCOUNT/NAME form (example: zetic/whisper-tiny)."`
	ModelKey   string `json:"model_key" jsonschema:"Opaque model key from list_models."`
	ReportType string `json:"report_type" jsonschema:"Which report to read: general, llm, or package."`
}

// withReportTypeEnum advertises the accepted report types, so a wrong one is
// refused by the schema rather than by a 404 from a guessed endpoint.
func withReportTypeEnum(props map[string]*jsonschema.Schema) {
	props["report_type"].Enum = enumValues(reportGeneral, reportLLM, reportPackage)
}

// getModelReportHandler wraps the three .../models/{key}/reports/* endpoints.
// Unlike `melange report view`, it never probes: the caller names the report
// type, so one tool call is at most one request.
func getModelReportHandler(d Deps) mcp.ToolHandlerFor[modelReportArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in modelReportArgs) (*mcp.CallToolResult, any, error) {
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return toolError(err), nil, nil
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}
		status, httpResp, body, err := requestReport(ctx, g, in.ReportType, account, name, in.ModelKey)
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(status, httpResp, body); err != nil {
			return toolError(err), nil, nil
		}
		return rawResult(body), nil, nil
	}
}

// requestReport dispatches one report type to the generated client, returning
// the raw exchange so the response bytes reach the caller untouched. The
// schema already rejects unknown types; the default arm keeps that a legible
// argument error rather than a nil-response panic if the enum ever drifts.
func requestReport(ctx context.Context, g *gen.ClientWithResponses,
	reportType, account, name, modelKey string,
) (int, *http.Response, []byte, error) {
	switch reportType {
	case reportGeneral:
		resp, err := g.GetGeneralReportWithResponse(ctx, account, name, modelKey)
		if err != nil {
			return 0, nil, nil, err
		}
		return resp.StatusCode(), resp.HTTPResponse, resp.Body, nil
	case reportLLM:
		resp, err := g.GetLlmReportWithResponse(ctx, account, name, modelKey)
		if err != nil {
			return 0, nil, nil, err
		}
		return resp.StatusCode(), resp.HTTPResponse, resp.Body, nil
	case reportPackage:
		resp, err := g.GetPackageReportWithResponse(ctx, account, name, modelKey)
		if err != nil {
			return 0, nil, nil, err
		}
		return resp.StatusCode(), resp.HTTPResponse, resp.Body, nil
	}
	return 0, nil, nil, fmt.Errorf(
		"invalid report_type %q: expected general, llm, or package", reportType)
}
