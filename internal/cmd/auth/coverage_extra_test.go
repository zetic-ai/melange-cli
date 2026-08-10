package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gokeyring "github.com/zalando/go-keyring"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
	"github.com/zetic-ai/melange-cli/internal/oauth"
)

func TestStoreOAuthAndToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	gokeyring.MockInit()
	cfg, err := config.Load()
	require.NoError(t, err)
	host := &hostContext{cfg: cfg, host: config.Resolved{Value: "https://api.zetic.ai"}, hostKey: "api.zetic.ai"}
	creds := config.OAuthCredentials{AccessToken: "zoa_test", RefreshToken: "zor_test", Expiry: time.Now().Add(time.Hour), ClientID: "cid", Scope: "write"}
	storage, err := storeOAuth(host, creds, false)
	require.NoError(t, err)
	assert.Equal(t, "keyring", storage)
	got, err := keyring.GetOAuth("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "zoa_test", got.AccessToken)
	host2 := &hostContext{cfg: cfg, host: config.Resolved{Value: "https://api.zetic.ai"}, hostKey: "api.zetic.ai"}
	storage2, err := storeToken(host2, "ztp_pat123", false)
	require.NoError(t, err)
	assert.Equal(t, "keyring", storage2)
}

func TestStorageLocationBranches(t *testing.T) {
	assert.Equal(t, "keyring", storageLocation("keyring"))
	assert.Equal(t, "keyring", storageLocation("oauth(keyring)"))
	assert.Contains(t, storageLocation("config"), "config.yml")
	assert.Contains(t, storageLocation("oauth(config)"), "config.yml")
	assert.Equal(t, "keyring", storageLocation("oauth"))
	assert.Equal(t, "keyring", storageLocation("oauth_custom"))
	assert.Equal(t, "environment (not stored)", storageLocation("env:MELANGE_API_KEY"))
	assert.Equal(t, "environment (not stored)", storageLocation("unknown"))
}

func TestScopeListBranches(t *testing.T) {
	assert.Equal(t, "none", scopeList(nil))
	assert.Equal(t, "none", scopeList([]string{}))
	assert.Equal(t, "read, write", scopeList([]string{"read", "write"}))
	assert.Equal(t, "write", scopeList([]string{"write"}))
}

func TestResolveHostError(t *testing.T) {
	f := &cmdutil.Factory{
		Config: func() (*config.Config, error) {
			return nil, fmt.Errorf("config load failed")
		},
	}
	_, err := resolveHost(f)
	require.Error(t, err)
	f2 := &cmdutil.Factory{
		Config: func() (*config.Config, error) {
			return &config.Config{Host: "https://api.zetic.ai"}, nil
		},
		HostOverride: "https://override.zetic.ai",
	}
	h, err := resolveHost(f2)
	require.NoError(t, err)
	assert.Equal(t, "https://override.zetic.ai", h.host.Value)
}

func TestResolveAnyTokenStaleRefresh(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv(config.EnvAPIKey, "")
	t.Setenv(config.EnvAPIKeyFile, "")
	gokeyring.MockInit()
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".well-known") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(discovery)
			return
		}
		if r.URL.Path == "/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "zoa_new",
				"refresh_token": "zor_new",
				"expires_in":    3600,
				"scope":         "write",
				"token_type":    "Bearer",
			})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	hostKey := "api.zetic.ai"
	stale := config.OAuthCredentials{AccessToken: "zoa_old", RefreshToken: "zor_old", Expiry: time.Now().Add(-time.Hour), ClientID: "cid"}
	require.NoError(t, keyring.SetOAuth(hostKey, stale))
	cfg := &config.Config{}
	transport := &redirectTransportForTest{target: srv.URL}
	h := &hostContext{cfg: cfg, host: config.Resolved{Value: srv.URL}, hostKey: hostKey, transport: transport}
	tok, creds, err := h.resolveAnyToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "zoa_new", tok.Value)
	require.NotNil(t, creds)
	assert.Equal(t, "zoa_new", creds.AccessToken)
}

func TestResolveAnyTokenInvalidGrant(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv(config.EnvAPIKey, "")
	t.Setenv(config.EnvAPIKeyFile, "")
	gokeyring.MockInit()
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".well-known") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(discovery)
			return
		}
		if r.URL.Path == "/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "expired"})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	stale := config.OAuthCredentials{AccessToken: "zoa_old", RefreshToken: "zor_old", Expiry: time.Now().Add(-time.Hour), ClientID: "cid"}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", stale))
	cfg := &config.Config{}
	transport := &redirectTransportForTest{target: srv.URL}
	h := &hostContext{cfg: cfg, host: config.Resolved{Value: srv.URL}, hostKey: "api.zetic.ai", transport: transport}
	_, _, err := h.resolveAnyToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session expired")
	_, err = keyring.GetOAuth("api.zetic.ai")
	assert.Error(t, err)
}

type redirectTransportForTest struct {
	target string
}

func (r *redirectTransportForTest) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL := req.URL.String()
	if strings.Contains(newURL, "api.zetic.ai") {
		u := r.target + req.URL.Path
		if req.URL.RawQuery != "" {
			u += "?" + req.URL.RawQuery
		}
		req2, _ := http.NewRequestWithContext(req.Context(), req.Method, u, req.Body)
		req2.Header = req.Header
		return http.DefaultTransport.RoundTrip(req2)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func init() {
	_ = filepath.Join
	_ = os.Getenv
	_ = time.Now
	_ = oauth.DefaultScope
}
