package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

func TestOAuthError(t *testing.T) {
	e1 := &OAuthError{Code: "invalid_grant", Description: "expired"}
	assert.Equal(t, "invalid_grant: expired", e1.Error())
	e2 := &OAuthError{Code: "invalid_grant"}
	assert.Equal(t, "invalid_grant", e2.Error())
}

func TestHttpClientWithTransportNil(t *testing.T) {
	c := httpClientWithTransport(nil)
	require.NotNil(t, c)
	assert.Equal(t, http.DefaultTransport, c.Transport)
	c2 := httpClientWithTransport(http.DefaultTransport)
	assert.Equal(t, http.DefaultTransport, c2.Transport)
}

func TestDiscoverWithRedirectTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".well-known") {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
				"token_endpoint":         "https://api.zetic.ai/oauth/token",
				"registration_endpoint":  "https://api.zetic.ai/oauth/register",
				"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	tr := &redirectTransport{target: srv.URL, base: srv.Client().Transport}
	d, err := DiscoverWithTransport(context.Background(), "https://api.zetic.ai", tr)
	require.NoError(t, err)
	assert.Equal(t, "https://api.zetic.ai/oauth/authorize", d.AuthorizationEndpoint)
}

type redirectTransport struct {
	target string
	base   http.RoundTripper
}

func (r *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL := req.URL.String()
	if strings.Contains(newURL, "api.zetic.ai") {
		base := r.base
		if base == nil {
			base = http.DefaultTransport
		}
		req2, _ := http.NewRequestWithContext(req.Context(), req.Method, r.target+req.URL.Path, req.Body)
		if req.URL.RawQuery != "" {
			req2.URL.RawQuery = req.URL.RawQuery
		}
		req2.Header = req.Header
		return base.RoundTrip(req2)
	}
	if r.base != nil {
		return r.base.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestPortFromListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := portFromListener(ln)
	assert.Greater(t, port, 0)
	mock := &mockListener{}
	assert.Equal(t, 0, portFromListener(mock))
}

type mockListener struct{}

func (m *mockListener) Accept() (net.Conn, error) { return nil, fmt.Errorf("not impl") }
func (m *mockListener) Close() error              { return nil }
func (m *mockListener) Addr() net.Addr            { return &mockAddr{} }

type mockAddr struct{}

func (m *mockAddr) Network() string { return "tcp" }
func (m *mockAddr) String() string  { return "mock:1234" }

func TestLoginFlowExplicitTransportCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := LoginFlowWithTransport(ctx, "https://example.invalid", nil, &httpmock.Registry{})
	assert.Error(t, err)
	_, err = LoginFlowWithOptionsWithTransport(ctx, "https://example.invalid", nil, true, &httpmock.Registry{})
	assert.Error(t, err)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ln.Close()
	_, err = doLoginAttemptWithTransport(ctx, &Discovery{AuthorizationEndpoint: "https://example.invalid/oauth/authorize", TokenEndpoint: "https://example.invalid/oauth/token", RegistrationEndpoint: "https://example.invalid/oauth/register"}, "https://example.invalid", "cid", "http://127.0.0.1:1234/callback", ln, nil, true, true, &httpmock.Registry{})
	assert.Error(t, err)
}

func TestGenerateVerifierNotEmpty(t *testing.T) {
	v, err := generateVerifier()
	require.NoError(t, err)
	assert.Len(t, v, 43)
	s, err := generateState()
	require.NoError(t, err)
	assert.NotEmpty(t, s)
	assert.NotContains(t, v, "+")
	assert.NotContains(t, v, "/")
	assert.NotContains(t, v, "=")
}

func TestDiscoverWithTransportInvalidJSON(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(func(req *http.Request) bool {
		return strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.StatusStringResponse(200, "not json"))
	_, err := DiscoverWithTransport(context.Background(), "https://api.zetic.ai", reg)
	require.Error(t, err)
}

func TestExchangeCodeNonInvalidTarget(t *testing.T) {
	reg := &httpmock.Registry{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
	}
	reg.Register(func(req *http.Request) bool {
		return strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.URL.Path == "/oauth/token"
	}, httpmock.StatusStringResponse(400, `not json error`))
	_, err := ExchangeCodeWithTransport(context.Background(), "https://api.zetic.ai", "cid", "code", "ver", "http://127.0.0.1:1234/callback", "", reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token exchange 400")
}

func TestRefreshNonOAuthError(t *testing.T) {
	reg := &httpmock.Registry{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
	}
	reg.Register(func(req *http.Request) bool {
		return strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.URL.Path == "/oauth/token"
	}, httpmock.StatusStringResponse(400, `not json`))
	_, err := RefreshWithTransport(context.Background(), "https://api.zetic.ai", "cid", "tok", reg)
	require.Error(t, err)
}

func TestNormalizeHost(t *testing.T) {
	assert.Equal(t, "https://api.zetic.ai", normalizeHost("https://api.zetic.ai/"))
	assert.Equal(t, "https://api.zetic.ai", normalizeHost("  https://api.zetic.ai/ "))
}
