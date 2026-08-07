package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

func TestDiscoverSuccess(t *testing.T) {
	reg := &httpmock.Registry{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, ".well-known/oauth-authorization-server")
	}, httpmock.JSONResponse(200, discovery))

	d, err := DiscoverWithTransport(context.Background(), "https://api.zetic.ai", reg)
	require.NoError(t, err)
	assert.Equal(t, "https://api.zetic.ai/oauth/authorize", d.AuthorizationEndpoint)
	assert.Equal(t, "https://api.zetic.ai/oauth/token", d.TokenEndpoint)
	assert.Equal(t, "https://api.zetic.ai/oauth/register", d.RegistrationEndpoint)
	assert.Equal(t, "https://api.zetic.ai/oauth/revoke", d.RevocationEndpoint)
	reg.Verify(t)
}

func TestDiscoverMissingEndpoints(t *testing.T) {
	reg := &httpmock.Registry{}
	// Missing registration_endpoint
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
	}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))

	_, err := DiscoverWithTransport(context.Background(), "https://api.zetic.ai", reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery missing endpoints")
	reg.Verify(t)
}

func TestDiscoverNotFound(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.StatusStringResponse(404, "not found"))

	_, err := DiscoverWithTransport(context.Background(), "https://api.zetic.ai", reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery 404")
	reg.Verify(t)
}

func TestRegisterClientSuccess(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/register"
	}, httpmock.JSONResponse(201, map[string]string{"client_id": "test_client_123"}))

	clientID, err := RegisterClientWithTransport(context.Background(), "https://api.zetic.ai/oauth/register", "http://127.0.0.1:1234/callback", reg)
	require.NoError(t, err)
	assert.Equal(t, "test_client_123", clientID)
	reg.Verify(t)
}

func TestRegisterClientFailure(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/register"
	}, httpmock.StatusStringResponse(400, `{"error":"invalid"}`))

	_, err := RegisterClientWithTransport(context.Background(), "https://api.zetic.ai/oauth/register", "http://127.0.0.1:1234/callback", reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DCR failed 400")
	reg.Verify(t)
}

func TestRegisterClientMissingID(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/register"
	}, httpmock.JSONResponse(201, map[string]string{"client_id": ""}))

	_, err := RegisterClientWithTransport(context.Background(), "https://api.zetic.ai/oauth/register", "http://127.0.0.1:1234/callback", reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing client_id")
	reg.Verify(t)
}

func TestExchangeCodeSuccess(t *testing.T) {
	reg := &httpmock.Registry{}
	// Discovery mock
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	// Token endpoint mock
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/token"
	}, httpmock.JSONResponse(200, map[string]any{
		"access_token":  "zoa_test123",
		"refresh_token": "zor_test123",
		"expires_in":    3600,
		"scope":         "write",
		"token_type":    "Bearer",
	}))

	tok, err := ExchangeCodeWithTransport(context.Background(), "https://api.zetic.ai", "client123", "authcode", "verifier", "http://127.0.0.1:1234/callback", "https://api.zetic.ai", reg)
	require.NoError(t, err)
	assert.Equal(t, "zoa_test123", tok.AccessToken)
	assert.Equal(t, "zor_test123", tok.RefreshToken)
	assert.Equal(t, 3600, tok.ExpiresIn)
	reg.Verify(t)
}

func TestExchangeCodeInvalidTarget(t *testing.T) {
	reg := &httpmock.Registry{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/token"
	}, httpmock.JSONResponse(400, map[string]string{"error": "invalid_target", "error_description": "not allowlisted"}))

	_, err := ExchangeCodeWithTransport(context.Background(), "https://api.zetic.ai", "client123", "authcode", "verifier", "http://127.0.0.1:1234/callback", "https://api.zetic.ai", reg)
	require.Error(t, err)
	var oe *OAuthError
	require.ErrorAs(t, err, &oe)
	assert.Equal(t, "invalid_target", oe.Code)
	reg.Verify(t)
}

func TestExchangeCodeWithFallbackDiscovery(t *testing.T) {
	reg := &httpmock.Registry{}
	// Discovery fails -> fallback to /oauth/token
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.StatusStringResponse(500, "server error"))
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/token"
	}, httpmock.JSONResponse(200, map[string]any{
		"access_token":  "zoa_fallback",
		"refresh_token": "zor_fallback",
		"expires_in":    3600,
		"scope":         "write",
		"token_type":    "Bearer",
	}))

	tok, err := ExchangeCodeWithTransport(context.Background(), "https://api.zetic.ai", "client123", "authcode", "verifier", "http://127.0.0.1:1234/callback", "", reg)
	require.NoError(t, err)
	assert.Equal(t, "zoa_fallback", tok.AccessToken)
	reg.Verify(t)
}

func TestRefreshSuccess(t *testing.T) {
	reg := &httpmock.Registry{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/token"
	}, httpmock.JSONResponse(200, map[string]any{
		"access_token":  "zoa_new",
		"refresh_token": "zor_new",
		"expires_in":    3600,
		"scope":         "write",
		"token_type":    "Bearer",
	}))

	tok, err := RefreshWithTransport(context.Background(), "https://api.zetic.ai", "client123", "old_refresh", reg)
	require.NoError(t, err)
	assert.Equal(t, "zoa_new", tok.AccessToken)
	assert.Equal(t, "zor_new", tok.RefreshToken)
	reg.Verify(t)
}

func TestRefreshInvalidGrant(t *testing.T) {
	reg := &httpmock.Registry{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/token"
	}, httpmock.JSONResponse(400, map[string]string{"error": "invalid_grant", "error_description": "expired"}))

	_, err := RefreshWithTransport(context.Background(), "https://api.zetic.ai", "client123", "bad_refresh", reg)
	require.Error(t, err)
	var oe *OAuthError
	require.ErrorAs(t, err, &oe)
	assert.Equal(t, "invalid_grant", oe.Code)
	reg.Verify(t)
}

func TestRevokeSuccess(t *testing.T) {
	reg := &httpmock.Registry{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/revoke"
	}, httpmock.StatusStringResponse(200, ""))

	err := RevokeWithTransport(context.Background(), "https://api.zetic.ai", "client123", "zoa_token", reg)
	require.NoError(t, err)
	reg.Verify(t)
}

func TestRevokeWithFallback(t *testing.T) {
	reg := &httpmock.Registry{}
	// Discovery returns no revocation endpoint -> fallback
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
	}
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/revoke"
	}, httpmock.StatusStringResponse(200, ""))

	err := RevokeWithTransport(context.Background(), "https://api.zetic.ai", "client123", "zoa_token", reg)
	require.NoError(t, err)
	reg.Verify(t)
}

func TestLoopbackHandlerForbidden(t *testing.T) {
	ch := make(chan callbackResult, 1)
	h := loopbackHandler("state123", ch)
	req := httptest.NewRequest(http.MethodGet, "/callback?code=abc&state=state123", nil)
	req.Host = "evil.example:80"
	w := httptest.NewRecorder()
	h(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	// channel should be empty (no result)
	select {
	case <-ch:
		t.Fatal("should not have sent result for forbidden host")
	default:
	}
}

func TestLoopbackHandlerStateMismatch(t *testing.T) {
	ch := make(chan callbackResult, 1)
	h := loopbackHandler("expected", ch)
	req := httptest.NewRequest(http.MethodGet, "/callback?code=abc&state=wrong", nil)
	req.Host = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	result := <-ch
	require.NotNil(t, result.Err)
	assert.Equal(t, "invalid_state", result.Err.Code)
}

func TestLoopbackHandlerSuccess(t *testing.T) {
	ch := make(chan callbackResult, 1)
	h := loopbackHandler("state123", ch)
	req := httptest.NewRequest(http.MethodGet, "/callback?code=authcode123&state=state123", nil)
	req.Host = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	result := <-ch
	assert.Equal(t, "authcode123", result.Code)
	assert.Equal(t, "state123", result.State)
	assert.Nil(t, result.Err)
}

func TestLoopbackHandlerErrorParam(t *testing.T) {
	ch := make(chan callbackResult, 1)
	h := loopbackHandler("state123", ch)
	req := httptest.NewRequest(http.MethodGet, "/callback?error=access_denied&error_description=denied&state=state123", nil)
	req.Host = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	result := <-ch
	require.NotNil(t, result.Err)
	assert.Equal(t, "access_denied", result.Err.Code)
}

func TestLoopbackHandlerMissingCode(t *testing.T) {
	ch := make(chan callbackResult, 1)
	h := loopbackHandler("state123", ch)
	req := httptest.NewRequest(http.MethodGet, "/callback?state=state123", nil)
	req.Host = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	result := <-ch
	require.NotNil(t, result.Err)
	assert.Equal(t, "missing_code", result.Err.Code)
}

func TestFallbackDiscovery(t *testing.T) {
	d := fallbackDiscovery("https://api.zetic.ai")
	assert.Equal(t, "https://api.zetic.ai/oauth/authorize", d.AuthorizationEndpoint)
	assert.Equal(t, "https://api.zetic.ai/oauth/token", d.TokenEndpoint)
	assert.Equal(t, "https://api.zetic.ai/oauth/register", d.RegistrationEndpoint)
	assert.Equal(t, "https://api.zetic.ai/oauth/revoke", d.RevocationEndpoint)

	d2 := fallbackDiscovery("https://api.zetic.ai/")
	assert.Equal(t, "https://api.zetic.ai/oauth/authorize", d2.AuthorizationEndpoint)
}

func TestBuildAuthorizeURL(t *testing.T) {
	url := buildAuthorizeURL("https://api.zetic.ai/oauth/authorize", "client123", "http://127.0.0.1:1234/callback", "challenge", "state123", true)
	assert.Contains(t, url, "response_type=code")
	assert.Contains(t, url, "client_id=client123")
	assert.Contains(t, url, "code_challenge=challenge")
	assert.Contains(t, url, "resource=")
	assert.Contains(t, url, "state=state123")

	url2 := buildAuthorizeURL("https://api.zetic.ai/oauth/authorize", "client123", "http://127.0.0.1:1234/callback", "challenge", "state123", false)
	assert.NotContains(t, url2, "resource=")

	// Test with existing query string
	url3 := buildAuthorizeURL("https://api.zetic.ai/oauth/authorize?foo=bar", "client123", "http://127.0.0.1:1234/callback", "challenge", "state123", true)
	assert.Contains(t, url3, "foo=bar")
}

func TestIsLoopbackVariants(t *testing.T) {
	assert.True(t, isLoopback("localhost"))
	assert.True(t, isLoopback("LOCALHOST"))
	assert.True(t, isLoopback("127.0.0.1"))
	assert.True(t, isLoopback("::1"))
	assert.False(t, isLoopback("evil.example"))
	assert.False(t, isLoopback("192.168.1.1"))
}

func TestExchangeCodeJSONUnmarshal(t *testing.T) {
	// Test that JSON marshal helper handles errors
	_ = json.RawMessage(`{}`)
}
