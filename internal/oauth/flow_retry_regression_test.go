package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// TestLoginFlowOuterInvalidTargetRetry verifies the hard-blocker fix at flow.go:76
// (ln.Close() before ln2). Without the fix, LoginFlowWithOptionsWithTransport
// always fails the mandatory authorize retry (allowlist [https://mcp.zetic.ai])
// with ErrLoopbackListen: address already in use because Serve(ln)+server.Close()
// does not free the custom net.Listener. This test reproduces the prod path:
// first callback returns ?error=invalid_target, outer retry must re-bind same port
// and succeed.
func TestLoginFlowOuterInvalidTargetRetry(t *testing.T) {
	reg := &httpmock.Registry{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	// Discovery for both outer and inner attempts (Register + Exchange)
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/register"
	}, httpmock.JSONResponse(201, map[string]string{"client_id": "retry_outer_cid"}))
	// Second discovery for ExchangeCode inside second attempt
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(200, discovery))
	reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodPost && req.URL.Path == "/oauth/token"
	}, httpmock.JSONResponse(200, map[string]any{
		"access_token":  "zoa_outer_retry",
		"refresh_token": "zor_outer_retry",
		"expires_in":    3600,
		"scope":         "write",
		"token_type":    "Bearer",
	}))

	var out safeBuffer
	credsCh := make(chan struct {
		creds interface{}
		err   error
	}, 1)
	go func() {
		creds, err := LoginFlowWithOptionsWithTransport(context.Background(), "https://api.zetic.ai", &out, true, reg)
		credsCh <- struct {
			creds interface{}
			err   error
		}{creds, err}
	}()

	// Wait for first auth URL
	var authURL1 string
	require.Eventually(t, func() bool {
		s := out.String()
		if strings.Contains(s, "https://") {
			for _, line := range strings.Split(s, "\n") {
				if strings.Contains(line, "https://") {
					idx := strings.Index(line, "https://")
					if idx >= 0 {
						authURL1 = strings.TrimSpace(line[idx:])
						return true
					}
				}
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	require.NotEmpty(t, authURL1)
	u1, err := url.Parse(authURL1)
	require.NoError(t, err)
	redirectURI := u1.Query().Get("redirect_uri")
	require.NotEmpty(t, redirectURI)
	state1 := u1.Query().Get("state")
	require.NotEmpty(t, state1)

	// First callback: simulate AS 302 ?error=invalid_target to loopback (prod allowlist hit)
	// Handler will return Err invalid_target, triggering outer retry
	invalidURL := redirectURI + "?error=invalid_target&error_description=not+allowlisted&state=" + url.QueryEscape(state1)
	resp, err := http.Get(invalidURL)
	require.NoError(t, err)
	resp.Body.Close()

	// Wait for second auth URL (retry without resource) — outer retry must have bound same port via ln2
	var authURL2 string
	require.Eventually(t, func() bool {
		s := out.String()
		// Count occurrences of "https://" — second should appear after retry message
		count := strings.Count(s, "https://")
		if count >= 2 {
			// Find last auth URL
			lines := strings.Split(s, "\n")
			for i := len(lines) - 1; i >= 0; i-- {
				if strings.Contains(lines[i], "https://") {
					idx := strings.Index(lines[i], "https://")
					if idx >= 0 {
						candidate := strings.TrimSpace(lines[i][idx:])
						if candidate != authURL1 {
							authURL2 = candidate
							return true
						}
					}
				}
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	require.NotEmpty(t, authURL2, "outer invalid_target retry must re-bind same port via ln2 and print second auth URL — without ln.Close() fix this never appears and returns ErrLoopbackListen")
	assert.Contains(t, out.String(), "not allowlisted")
	// Second callback: success with code
	u2, err := url.Parse(authURL2)
	require.NoError(t, err)
	state2 := u2.Query().Get("state")
	redirectURI2 := u2.Query().Get("redirect_uri")
	require.NotEmpty(t, state2)
	require.NotEmpty(t, redirectURI2)
	// Use the retry's redirectURI (same port, new state)
	callbackOK := redirectURI2 + "?code=outer_retry_code&state=" + url.QueryEscape(state2)
	resp2, err := http.Get(callbackOK)
	require.NoError(t, err)
	resp2.Body.Close()

	select {
	case result := <-credsCh:
		require.NoError(t, result.err, "outer retry must succeed after ln.Close() fix — without fix this was ErrLoopbackListen: address already in use")
		require.NotNil(t, result.creds)
		// Verify via JSON to avoid import cycle
		b, _ := json.Marshal(result.creds)
		var m map[string]interface{}
		_ = json.Unmarshal(b, &m)
		// At least ensure not nil
		assert.NotNil(t, result.creds)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for LoginFlow outer retry — ln.Close() fix missing, ln2 bind failed")
	}
	reg.Verify(t)
}
