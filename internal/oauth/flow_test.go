package oauth

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// safeBuffer is a thread-safe bytes.Buffer for concurrent fmt.Fprintf in flow
// vs Eventually String() reads under -race.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestDoLoginAttemptSuccess(t *testing.T) {
	reg := &httpmock.Registry{}
	// Discovery for token endpoint
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
		"access_token":  "zoa_test123",
		"refresh_token": "zor_test123",
		"expires_in":    3600,
		"scope":         "write",
		"token_type":    "Bearer",
	}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	redirectURI := "http://" + ln.Addr().String() + "/callback"
	// Use fallback discovery for simplicity
	disc := fallbackDiscovery("https://api.zetic.ai")
	disc.AuthorizationEndpoint = "https://api.zetic.ai/oauth/authorize"

	// We need to capture state from the printed auth URL. Run doLoginAttempt in goroutine
	var out safeBuffer
	// Use noBrowser true to avoid browser open
	credsCh := make(chan struct {
		creds *config.OAuthCredentials
		err   error
	}, 1)
	go func() {
		creds, err := doLoginAttemptWithTransport(context.Background(), disc, "https://api.zetic.ai", "test_client", redirectURI, ln, &out, true, true, reg)
		credsCh <- struct {
			creds *config.OAuthCredentials
			err   error
		}{creds, err}
	}()

	// Wait for server to start and URL to be printed
	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), "https://")
	}, 2*time.Second, 10*time.Millisecond)

	// Parse state from printed URL
	output := out.String()
	// Find first https URL
	var authURL string
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "https://") {
			// line is "Opening https://..."
			idx := strings.Index(line, "https://")
			if idx >= 0 {
				authURL = strings.TrimSpace(line[idx:])
				break
			}
		}
	}
	require.NotEmpty(t, authURL, "should have printed auth URL")
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	state := u.Query().Get("state")
	require.NotEmpty(t, state)

	// Send callback
	callbackURL := redirectURI + "?code=authcode123&state=" + url.QueryEscape(state)
	resp, err := http.Get(callbackURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Wait for creds
	select {
	case result := <-credsCh:
		require.NoError(t, result.err)
		require.NotNil(t, result.creds)
		assert.Equal(t, "zoa_test123", result.creds.AccessToken)
		assert.Equal(t, "zor_test123", result.creds.RefreshToken)
		assert.Equal(t, "test_client", result.creds.ClientID)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for doLoginAttempt")
	}
	reg.Verify(t)
}

func TestDoLoginAttemptInvalidTargetRetry(t *testing.T) {
	reg := &httpmock.Registry{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	// First discovery for withResource=true, second for retry without resource (both succeed)
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	// First token exchange with resource -> invalid_target
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/token"
	}, httpmock.JSONResponse(400, map[string]string{"error": "invalid_target", "error_description": "not allowlisted"}))
	// Second token exchange without resource -> success
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/token"
	}, httpmock.JSONResponse(200, map[string]any{
		"access_token":  "zoa_retry",
		"refresh_token": "zor_retry",
		"expires_in":    3600,
		"scope":         "write",
		"token_type":    "Bearer",
	}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	redirectURI := "http://127.0.0.1:" + strings.Split(ln.Addr().String(), ":")[1] + "/callback"
	disc := fallbackDiscovery("https://api.zetic.ai")

	var out safeBuffer
	credsCh := make(chan struct {
		creds *config.OAuthCredentials
		err   error
	}, 1)
	go func() {
		creds, err := doLoginAttemptWithTransport(context.Background(), disc, "https://api.zetic.ai", "test_client", redirectURI, ln, &out, true, true, reg)
		credsCh <- struct {
			creds *config.OAuthCredentials
			err   error
		}{creds, err}
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), "https://")
	}, 2*time.Second, 10*time.Millisecond)

	output := out.String()
	var authURL string
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "https://") {
			idx := strings.Index(line, "https://")
			if idx >= 0 {
				authURL = strings.TrimSpace(line[idx:])
				break
			}
		}
	}
	require.NotEmpty(t, authURL)
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	state := u.Query().Get("state")
	require.NotEmpty(t, state)

	callbackURL := redirectURI + "?code=authcode123&state=" + url.QueryEscape(state)
	resp, err := http.Get(callbackURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	select {
	case result := <-credsCh:
		require.NoError(t, result.err)
		require.NotNil(t, result.creds)
		assert.Equal(t, "zoa_retry", result.creds.AccessToken)
		// Check that retry message was printed
		assert.Contains(t, out.String(), "not allowlisted")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	reg.Verify(t)
}

func TestLoginFlowWithTransportIntegration(t *testing.T) {
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
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/register"
	}, httpmock.JSONResponse(201, map[string]string{"client_id": "integration_client"}))
	// Need second discovery for token exchange (ExchangeCode does Discover)
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/token"
	}, httpmock.JSONResponse(200, map[string]any{
		"access_token":  "zoa_integration",
		"refresh_token": "zor_integration",
		"expires_in":    3600,
		"scope":         "write",
		"token_type":    "Bearer",
	}))

	var out safeBuffer
	credsCh := make(chan struct {
		creds *config.OAuthCredentials
		err   error
	}, 1)
	go func() {
		creds, err := LoginFlowWithOptionsWithTransport(context.Background(), "https://api.zetic.ai", &out, true, reg)
		credsCh <- struct {
			creds *config.OAuthCredentials
			err   error
		}{creds, err}
	}()

	// Wait for auth URL printed and extract state and port
	var authURL string
	require.Eventually(t, func() bool {
		s := out.String()
		if strings.Contains(s, "https://") {
			for _, line := range strings.Split(s, "\n") {
				if strings.Contains(line, "https://") {
					idx := strings.Index(line, "https://")
					if idx >= 0 {
						authURL = strings.TrimSpace(line[idx:])
						return true
					}
				}
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)

	u, err := url.Parse(authURL)
	require.NoError(t, err)
	state := u.Query().Get("state")
	redirectURI := u.Query().Get("redirect_uri")
	require.NotEmpty(t, state)
	require.NotEmpty(t, redirectURI)
	// Send callback to redirectURI
	callbackURL := redirectURI + "?code=authcode123&state=" + url.QueryEscape(state)
	resp, err := http.Get(callbackURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	select {
	case result := <-credsCh:
		require.NoError(t, result.err)
		require.NotNil(t, result.creds)
		assert.Equal(t, "zoa_integration", result.creds.AccessToken)
		assert.Equal(t, "integration_client", result.creds.ClientID)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for LoginFlow")
	}
	reg.Verify(t)
}

func TestInvalidTargetRetry(t *testing.T) {
	// Wrapper for verification: covers both authorize+token retry via doLoginAttempt
	TestDoLoginAttemptInvalidTargetRetry(t)
	// Also verify ExchangeCode invalid_target path
	TestExchangeCodeInvalidTarget(t)
}
