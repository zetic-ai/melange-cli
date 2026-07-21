package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// Gen returns a generated OpenAPI client bound to this client's host that
// rides the same transport chain (auth -> retry -> debug), so every generated
// call carries the Bearer token, User-Agent, and retry behavior.
//
// Convert non-2xx generated responses with ErrorFrom:
//
//	if err := api.ErrorFrom(resp.StatusCode(), resp.HTTPResponse.Header, resp.Body); err != nil {
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
	if err := ErrorFrom(resp.StatusCode(), respHeader(resp.HTTPResponse), resp.Body); err != nil {
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

// respHeader tolerates generated responses without an HTTP response attached.
func respHeader(resp *http.Response) http.Header {
	if resp == nil {
		return nil
	}
	return resp.Header
}
