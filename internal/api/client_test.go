package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

// TestGetMe exercises the generated-client path: the wire call goes through
// gen.ClientWithResponses but reuses the wrapper's transport chain, so the
// Bearer token and User-Agent still apply.
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
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.JSONResponse(200, json.RawMessage(body)))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_secret", reg, nil)
	me, err := client.GetMe(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "dev@zetic.ai", me.User.Email)
	assert.Equal(t, "dev", me.User.Nickname)
	assert.Equal(t, "Zetic", me.Account.Name)
	assert.Equal(t, "org", me.Account.Type)
	assert.Equal(t, "ci-token", me.Token.Name)
	assert.Equal(t, []string{"repo:read", "model:write"}, me.Token.Scopes)
	require.NotNil(t, me.Token.ExpiresAt)
	assert.Equal(t, "2027-01-01T00:00:00Z", *me.Token.ExpiresAt)

	require.Len(t, reg.Requests, 1)
	got := reg.Requests[0]
	assert.Equal(t, "Bearer ztp_secret", got.Header.Get("Authorization"),
		"gen client must ride the wrapper's auth transport")
	assert.Equal(t, testUA, got.Header.Get("User-Agent"))
}

func TestGetMeNon2xxReturnsAPIError(t *testing.T) {
	reg := &httpmock.Registry{}
	body := `{"type":"error","error":{"type":"authentication_error","message":"invalid token"},"request_id":"req_7"}`
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(401, body))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_bad", reg, nil)
	_, err := client.GetMe(context.Background())
	require.Error(t, err)

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.StatusCode)
	assert.Equal(t, "authentication_error", apiErr.Type)
	assert.Equal(t, "req_7", apiErr.RequestID)
}

func TestErrorFrom(t *testing.T) {
	t.Run("2xx is nil", func(t *testing.T) {
		assert.NoError(t, api.ErrorFrom(200, nil, []byte(`{}`)))
		assert.NoError(t, api.ErrorFrom(201, nil, nil))
	})

	t.Run("envelope body", func(t *testing.T) {
		body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"name taken",` +
			`"fields":[{"field":"name","message":"already exists"}]},"request_id":"req_9"}`)
		err := api.ErrorFrom(409, nil, body)
		var apiErr *api.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 409, apiErr.StatusCode)
		assert.Equal(t, "invalid_request_error", apiErr.Type)
		assert.Equal(t, "name taken", apiErr.Message)
		require.Len(t, apiErr.Fields, 1)
		assert.Equal(t, "name", apiErr.Fields[0].Field)
	})

	t.Run("non-envelope body falls back to status", func(t *testing.T) {
		header := http.Header{}
		header.Set("X-Request-ID", "req_lb")
		err := api.ErrorFrom(503, header, []byte("upstream connect error"))
		var apiErr *api.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, "http_error", apiErr.Type)
		assert.Equal(t, "upstream connect error", apiErr.Message)
		assert.Equal(t, "req_lb", apiErr.RequestID)
	})
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

func TestClientDoPathWithQuery(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos"), httpmock.StatusStringResponse(200, "{}"))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_secret", reg, nil)
	resp, err := client.Do(context.Background(), "GET", "/v1/repos?limit=5&search=whisper", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, reg.Requests, 1)
	got := reg.Requests[0].URL
	assert.Equal(t, "/v1/repos", got.Path)
	assert.Equal(t, "limit=5&search=whisper", got.RawQuery,
		"a query string in the path must survive as a real query, not a path segment")
}

func TestClientCrossHostRedirectStripsAuthorization(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/artifacts/download"),
		httpmock.WithHeader(httpmock.StatusStringResponse(302, ""),
			"Location", "https://storage.example.com/blob"))
	reg.Register(httpmock.REST("GET", "/blob"), httpmock.StatusStringResponse(200, "DATA"))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_secret", reg, nil)
	resp, err := client.Do(context.Background(), "GET", "/v1/artifacts/download", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "DATA", string(body), "the redirect must still be followed")

	require.Len(t, reg.Requests, 2)
	assert.Equal(t, "Bearer ztp_secret", reg.Requests[0].Header.Get("Authorization"))
	assert.Equal(t, "storage.example.com", reg.Requests[1].URL.Host)
	assert.Empty(t, reg.Requests[1].Header.Get("Authorization"),
		"the host-bound token must never be sent to a foreign host")
}

func TestClientSameHostRedirectKeepsAuthorization(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/old"),
		httpmock.WithHeader(httpmock.StatusStringResponse(302, ""),
			"Location", "https://api.zetic.ai/v1/new"))
	reg.Register(httpmock.REST("GET", "/v1/new"), httpmock.StatusStringResponse(200, "ok"))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_secret", reg, nil)
	resp, err := client.Do(context.Background(), "GET", "/v1/old", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, reg.Requests, 2)
	assert.Equal(t, "Bearer ztp_secret", reg.Requests[1].Header.Get("Authorization"),
		"same-host redirects keep the credentials")
}

func TestClientRedirectToExplicitDefaultPortKeepsAuthorization(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/old"),
		httpmock.WithHeader(httpmock.StatusStringResponse(302, ""),
			"Location", "https://api.zetic.ai:443/v1/new"))
	reg.Register(httpmock.REST("GET", "/v1/new"), httpmock.StatusStringResponse(200, "ok"))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_secret", reg, nil)
	resp, err := client.Do(context.Background(), "GET", "/v1/old", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, reg.Requests, 2)
	assert.Equal(t, "Bearer ztp_secret", reg.Requests[1].Header.Get("Authorization"),
		"an explicit :443 is the same https host; the token must survive the redirect")
}

func TestClientRedirectToNonDefaultPortStripsAuthorization(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/old"),
		httpmock.WithHeader(httpmock.StatusStringResponse(302, ""),
			"Location", "https://api.zetic.ai:8443/v1/new"))
	reg.Register(httpmock.REST("GET", "/v1/new"), httpmock.StatusStringResponse(200, "ok"))

	client := newTestClient(t, "https://api.zetic.ai", "ztp_secret", reg, nil)
	resp, err := client.Do(context.Background(), "GET", "/v1/old", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, reg.Requests, 2)
	assert.Empty(t, reg.Requests[1].Header.Get("Authorization"),
		"a non-default port is a different origin; the token must not follow")
}

var _ http.RoundTripper = (*httpmock.Registry)(nil)
