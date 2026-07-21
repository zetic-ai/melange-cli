package api_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

const testUA = "melange-cli/test (darwin; arm64)"

func newTestClient(t *testing.T, host, token string, reg *httpmock.Registry, debug *bytes.Buffer) *api.Client {
	t.Helper()
	opts := api.Options{
		Host:      host,
		Token:     token,
		UserAgent: testUA,
		Transport: reg,
	}
	if debug != nil {
		opts.Debug = debug
	}
	client, err := api.NewClient(opts)
	require.NoError(t, err)
	return client
}

func TestClientSetsBearerAndUserAgent(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "{}"))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_secret", reg, nil)
	resp, err := client.Do(context.Background(), "GET", "/v1/me", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, reg.Requests, 1)
	got := reg.Requests[0]
	assert.Equal(t, "Bearer ztp_secret", got.Header.Get("Authorization"))
	assert.Equal(t, testUA, got.Header.Get("User-Agent"))
}

func TestClientNoTokenNoAuthorizationHeader(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "{}"))

	client := newTestClient(t, "https://api.zetic.ai", "", reg, nil)
	resp, err := client.Do(context.Background(), "GET", "/v1/me", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, reg.Requests, 1)
	assert.Empty(t, reg.Requests[0].Header.Get("Authorization"))
}

func TestClientRefusesTokenOverPlaintextHTTP(t *testing.T) {
	reg := &httpmock.Registry{}
	client := newTestClient(t, "http://example.com", "ztp_secret", reg, nil)

	_, err := client.Do(context.Background(), "GET", "/v1/me", nil, nil) //nolint:bodyclose
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to send credentials over plaintext HTTP")
	assert.Empty(t, reg.Requests, "no request must be sent")
}

func TestClientAllowsTokenOverLoopbackHTTP(t *testing.T) {
	for _, host := range []string{"http://127.0.0.1:8080", "http://localhost:3000", "http://[::1]:9999"} {
		t.Run(host, func(t *testing.T) {
			reg := &httpmock.Registry{}
			reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "{}"))

			client := newTestClient(t, host, "ztp_secret", reg, nil)
			resp, err := client.Do(context.Background(), "GET", "/v1/me", nil, nil)
			require.NoError(t, err)
			defer resp.Body.Close() //nolint:errcheck
			require.Len(t, reg.Requests, 1)
			assert.Equal(t, "Bearer ztp_secret", reg.Requests[0].Header.Get("Authorization"))
		})
	}
}

func TestClientHostWithoutSchemeDefaultsToHTTPS(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "{}"))

	client := newTestClient(t, "api.zetic.ai", "ztp_secret", reg, nil)
	resp, err := client.Do(context.Background(), "GET", "/v1/me", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, reg.Requests, 1)
	assert.Equal(t, "https", reg.Requests[0].URL.Scheme)
}

func TestClientJSONDecodesResponse(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/things"), httpmock.JSONResponse(200, map[string]string{"id": "t_1"}))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_secret", reg, nil)
	var out struct {
		ID string `json:"id"`
	}
	err := client.JSON(context.Background(), "POST", "/v1/things", map[string]string{"name": "x"}, &out)
	require.NoError(t, err)
	assert.Equal(t, "t_1", out.ID)

	require.Len(t, reg.Requests, 1)
	assert.Equal(t, "application/json", reg.Requests[0].Header.Get("Content-Type"))
}

func TestClientJSONReturnsAPIError(t *testing.T) {
	reg := &httpmock.Registry{}
	body := `{"type":"error","error":{"type":"authentication_error","message":"invalid token"},"request_id":"req_9"}`
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(401, body))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_bad", reg, nil)
	err := client.JSON(context.Background(), "GET", "/v1/me", nil, nil)
	require.Error(t, err)

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.StatusCode)
	assert.Equal(t, "authentication_error", apiErr.Type)
	assert.Equal(t, "req_9", apiErr.RequestID)
}

func TestGetMe(t *testing.T) {
	reg := &httpmock.Registry{}
	body := `{
		"user": {"email": "dev@zetic.ai", "nickname": "dev"},
		"account": {"name": "Zetic", "type": "org"},
		"token": {
			"name": "ci-token",
			"scopes": ["repo:read", "model:write"],
			"expires_at": "2027-01-01T00:00:00Z",
			"last_used_at": "2026-07-20T10:00:00Z"
		}
	}`
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, body))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_secret", reg, nil)
	me, err := client.GetMe(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "dev@zetic.ai", me.User.Email)
	assert.Equal(t, "dev", me.User.Nickname)
	assert.Equal(t, "Zetic", me.Account.Name)
	assert.Equal(t, "org", me.Account.Type)
	assert.Equal(t, "ci-token", me.Token.Name)
	assert.Equal(t, []string{"repo:read", "model:write"}, me.Token.Scopes)
	assert.Equal(t, "2027-01-01T00:00:00Z", me.Token.ExpiresAt)
}

func TestDebugOutputNeverContainsToken(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "{}"))

	debug := &bytes.Buffer{}
	client := newTestClient(t, "https://api.zetic.ai", "ztp_supersecret", reg, debug)
	resp, err := client.Do(context.Background(), "GET", "/v1/me", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	out := debug.String()
	assert.Contains(t, out, "> GET https://api.zetic.ai/v1/me")
	assert.Contains(t, out, "< 200")
	assert.NotContains(t, out, "ztp_supersecret", "token must never appear in debug output")
	assert.NotContains(t, out, "Authorization", "headers must not be dumped unredacted")
}

func TestClientCustomHeaders(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/things"), httpmock.StatusStringResponse(200, "{}"))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_secret", reg, nil)
	resp, err := client.Do(context.Background(), "POST", "/v1/things", nil,
		map[string]string{"Idempotency-Key": "idem-42"})
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, reg.Requests, 1)
	assert.Equal(t, "idem-42", reg.Requests[0].Header.Get("Idempotency-Key"))
}

var _ http.RoundTripper = (*httpmock.Registry)(nil)
