package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
)

// registerAccount registers the account/identity tools.
func registerAccount(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "whoami",
		Description: "Verify credentials and report the identity behind them: " +
			"user, account, and token name/scopes. Call this first to confirm " +
			"authentication before using the other melange tools.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: falsePtr(),
		},
	}, whoamiHandler(d))
}

// whoamiArgs is empty: whoami takes no arguments.
type whoamiArgs struct{}

// whoamiHandler wraps GET /v1/me as a raw-passthrough tool.
func whoamiHandler(d Deps) mcp.ToolHandlerFor[whoamiArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ whoamiArgs) (*mcp.CallToolResult, any, error) {
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}
		resp, err := g.GetMeWithResponse(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return toolError(err), nil, nil
		}
		return rawResult(resp.Body), nil, nil
	}
}
