package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gokeyring "github.com/zalando/go-keyring"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
)

func TestResolveAnyTokenMainFresh(t *testing.T) {
	gokeyring.MockInit()
	creds := config.OAuthCredentials{
		AccessToken:  "zoa_main_fresh",
		RefreshToken: "zor_main_fresh",
		Expiry:       time.Now().Add(time.Hour),
		ClientID:     "cid_main",
	}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", creds))
	cfg := &config.Config{}
	res, gotCreds, err := resolveAnyTokenMain(context.Background(), cfg, "https://api.zetic.ai", "api.zetic.ai", http.DefaultTransport)
	require.NoError(t, err)
	assert.Equal(t, "zoa_main_fresh", res.Value)
	require.NotNil(t, gotCreds)
}

func TestResolveAnyTokenMainStaleRefreshSuccess(t *testing.T) {
	gokeyring.MockInit()
	stale := config.OAuthCredentials{
		AccessToken:  "zoa_old_main",
		RefreshToken: "zor_old_main",
		Expiry:       time.Now().Add(-time.Hour),
		ClientID:     "cid_main",
	}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", stale))
	cfg := &config.Config{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".well-known") {
			_ = json.NewEncoder(w).Encode(discovery)
			return
		}
		if r.URL.Path == "/oauth/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "zoa_new_main",
				"refresh_token": "zor_new_main",
				"expires_in":    3600,
				"scope":         "write",
				"token_type":    "Bearer",
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	transport := &redirectTransportMain{target: srv.URL}
	res, gotCreds, err := resolveAnyTokenMain(context.Background(), cfg, srv.URL, "api.zetic.ai", transport)
	require.NoError(t, err)
	assert.Equal(t, "zoa_new_main", res.Value)
	require.NotNil(t, gotCreds)
}

func TestResolveAnyTokenMainStaleInvalidGrant(t *testing.T) {
	gokeyring.MockInit()
	stale := config.OAuthCredentials{
		AccessToken:  "zoa_old_main",
		RefreshToken: "zor_old_main",
		Expiry:       time.Now().Add(-time.Hour),
		ClientID:     "cid_main",
	}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", stale))
	cfg := &config.Config{}
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".well-known") {
			_ = json.NewEncoder(w).Encode(discovery)
			return
		}
		if r.URL.Path == "/oauth/token" {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	transport := &redirectTransportMain{target: srv.URL}
	_, _, err := resolveAnyTokenMain(context.Background(), cfg, srv.URL, "api.zetic.ai", transport)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session expired")
	// Should have cleared oauth
	_, err = keyring.GetOAuth("api.zetic.ai")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestResolveAnyTokenMainNoToken(t *testing.T) {
	gokeyring.MockInit()
	cfg := &config.Config{}
	res, creds, err := resolveAnyTokenMain(context.Background(), cfg, "https://api.zetic.ai", "api.zetic.ai", nil)
	require.NoError(t, err)
	assert.Equal(t, "", res.Value)
	assert.Nil(t, creds)
}

func TestResolveAnyTokenMainWithPATFallback(t *testing.T) {
	gokeyring.MockInit()
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_pat_main"))
	cfg := &config.Config{}
	res, _, err := resolveAnyTokenMain(context.Background(), cfg, "https://api.zetic.ai", "api.zetic.ai", http.DefaultTransport)
	require.NoError(t, err)
	assert.Equal(t, "ztp_pat_main", res.Value)
}

func TestMapCobraErrorBranches(t *testing.T) {
	assert.Nil(t, mapCobraError(nil))
	err := mapCobraError(assert.AnError)
	assert.NotNil(t, err)
	// Unknown command should map to FlagError
	unk := mapCobraError(assertAnError("unknown command foo"))
	assert.NotNil(t, unk)
	// Required flag
	req := mapCobraError(assertAnError("required flag --foo not set"))
	assert.NotNil(t, req)
}

func assertAnError(msg string) error {
	return &testErr{msg: msg}
}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }

type redirectTransportMain struct {
	target string
}

func (r *redirectTransportMain) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL := req.URL.String()
	if strings.Contains(newURL, "api.zetic.ai") || strings.Contains(req.URL.Host, "127.0.0.1") {
		u := r.target + req.URL.Path
		if req.URL.RawQuery != "" {
			u += "?" + req.URL.RawQuery
		}
		req2, _ := http.NewRequestWithContext(req.Context(), req.Method, u, req.Body)
		req2.Header = req.Header
		return http.DefaultTransport.RoundTrip(req2)
	}
	// If target contains the original host, redirect there
	if strings.HasPrefix(r.target, "http") {
		// Try to see if request host matches srv URL host, if not rewrite
		if req.URL.Host != "" {
			u := r.target + req.URL.Path
			if req.URL.RawQuery != "" {
				u += "?" + req.URL.RawQuery
			}
			req2, _ := http.NewRequestWithContext(req.Context(), req.Method, u, req.Body)
			req2.Header = req.Header
			return http.DefaultTransport.RoundTrip(req2)
		}
	}
	return http.DefaultTransport.RoundTrip(req)
}
