package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
)

// The sections get_account_info can assemble.
const (
	sectionUsage  = "usage"
	sectionQuotas = "quotas"
	sectionPlan   = "plan"
)

// registerAccount registers the account/identity tools.
func registerAccount(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "whoami",
		Description: "Verify credentials and report the identity behind them: " +
			"user, account, and token name/scopes. Call this first to confirm " +
			"authentication before using the other melange tools.",
		OutputSchema: outputSchema("whoami"),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, whoamiHandler(d))

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_account_info",
		Description: "Report the account's standing as the composite object " +
			`{"usage": …, "quotas": …, "plan": …}: current billing-period counters, ` +
			"those counters against the plan limits, and the effective plan tier. " +
			"'include' narrows it to the sections you need — the returned object then " +
			"carries only those keys. Check quotas before an upload or conversion: each " +
			"counter's 'remaining' is what enforcement actually permits right now.",
		InputSchema:  inputSchemaFor[accountInfoArgs](withSectionEnum),
		OutputSchema: outputSchema("get_account_info"),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, getAccountInfoHandler(d))
}

// whoamiArgs is empty: whoami takes no arguments.
type whoamiArgs struct{}

// whoamiHandler wraps GET /v1/me as a raw-passthrough tool.
func whoamiHandler(d Deps) mcp.ToolHandlerFor[whoamiArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ whoamiArgs) (*mcp.CallToolResult, any, error) {
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		resp, err := g.GetMeWithResponse(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return d.toolError(err), nil, nil
		}
		return rawResult(resp.Body)
	}
}

// accountInfoArgs are the arguments of get_account_info.
type accountInfoArgs struct {
	Include []string `json:"include,omitempty" jsonschema:"Sections to fetch: usage, quotas, plan. Omit for all three."`
}

// withSectionEnum advertises the section vocabulary, so a misspelled section
// is refused outright instead of quietly dropping data from the envelope.
func withSectionEnum(props map[string]*jsonschema.Schema) {
	props["include"].Items.Enum = enumValues(sectionUsage, sectionQuotas, sectionPlan)
}

// accountInfo is the get_account_info envelope. Each half stays
// json.RawMessage so the API responses survive byte-for-byte, and an
// unrequested section is absent rather than null.
type accountInfo struct {
	Usage  json.RawMessage `json:"usage,omitempty"`
	Quotas json.RawMessage `json:"quotas,omitempty"`
	Plan   json.RawMessage `json:"plan,omitempty"`
}

// getAccountInfoHandler composes GET /v1/usage, /v1/usage/quotas, and
// /v1/billing/plan. The three answer one question — "what may this account
// still do?" — so they are one tool rather than three round trips.
func getAccountInfoHandler(d Deps) mcp.ToolHandlerFor[accountInfoArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in accountInfoArgs) (*mcp.CallToolResult, any, error) {
		// An omitted include means every section: the default lives here, not in
		// the schema, because a schema default crashes the SDK on a literal
		// `"arguments": null` (see withPageBounds).
		wantUsage, wantQuotas, wantPlan := true, true, true
		if len(in.Include) > 0 {
			wantUsage, wantQuotas, wantPlan = false, false, false
			for _, section := range in.Include {
				switch section {
				case sectionUsage:
					wantUsage = true
				case sectionQuotas:
					wantQuotas = true
				case sectionPlan:
					wantPlan = true
				default:
					// Unreachable through the schema; kept so a drifting enum
					// reports the bad argument instead of an empty envelope.
					return d.toolError(fmt.Errorf(
						"invalid include %q: expected usage, quotas, or plan", section)), nil, nil
				}
			}
		}

		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}

		// Sections are fetched in envelope order regardless of how `include`
		// was written, so the same request always produces the same object.
		var info accountInfo
		if wantUsage {
			resp, err := g.GetUsageWithResponse(ctx)
			if err != nil {
				return d.toolError(err), nil, nil
			}
			if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
				return d.toolError(err), nil, nil
			}
			info.Usage = resp.Body
		}
		if wantQuotas {
			resp, err := g.GetUsageQuotasWithResponse(ctx)
			if err != nil {
				return d.toolError(err), nil, nil
			}
			if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
				return d.toolError(err), nil, nil
			}
			info.Quotas = resp.Body
		}
		if wantPlan {
			resp, err := g.GetBillingPlanWithResponse(ctx)
			if err != nil {
				return d.toolError(err), nil, nil
			}
			if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
				return d.toolError(err), nil, nil
			}
			info.Plan = resp.Body
		}

		envelope, err := marshalEnvelope(info)
		if err != nil {
			// Every half is API JSON we already accepted; a failure here is a
			// programming fault, not something the caller can act on.
			return nil, nil, fmt.Errorf("building get_account_info envelope: %w", err)
		}
		return rawResult(envelope)
	}
}
