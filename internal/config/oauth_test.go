package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gokeyring "github.com/zalando/go-keyring"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
)

func TestKeyringOAuthRoundTrip(t *testing.T) {
	gokeyring.MockInit()
	cfg := &config.Config{}
	creds := config.OAuthCredentials{
		AccessToken:  "zoa_test",
		RefreshToken: "zor_test",
		Expiry:       time.Now().Add(time.Hour),
		ClientID:     "abc123",
		Scope:        "write",
		TokenType:    "Bearer",
	}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", creds))
	got, err := keyring.GetOAuth("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, creds.AccessToken, got.AccessToken)
	assert.Equal(t, creds.RefreshToken, got.RefreshToken)
	assert.Equal(t, creds.ClientID, got.ClientID)

	// Config roundtrip with Expiry omitempty
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	cfg.Hosts = map[string]config.HostEntry{
		"api.zetic.ai": {OAuth: &creds, Storage: config.CredentialStorageConfig},
	}
	require.NoError(t, config.SaveTo(cfg, path))
	loaded, err := config.LoadFrom(path)
	require.NoError(t, err)
	require.NotNil(t, loaded.Hosts["api.zetic.ai"].OAuth)
	assert.Equal(t, creds.AccessToken, loaded.Hosts["api.zetic.ai"].OAuth.AccessToken)
	assert.False(t, loaded.Hosts["api.zetic.ai"].OAuth.Expiry.IsZero())
}

func TestKeyringPATAndOAuthAreSeparateKeys(t *testing.T) {
	gokeyring.MockInit()
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_pat"))
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", config.OAuthCredentials{AccessToken: "zoa_x", RefreshToken: "zor_x", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}))
	pat, err := keyring.Get("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "ztp_pat", pat)
	oauth, err := keyring.GetOAuth("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "zoa_x", oauth.AccessToken)
	// SetOAuth should clear PAT via store logic, but direct keyring Sets keep separate keys
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", config.OAuthCredentials{AccessToken: "zoa_y", RefreshToken: "zor_y", Expiry: time.Now().Add(time.Hour), ClientID: "cid2"}))
	_, err = keyring.Get("api.zetic.ai")
	assert.NoError(t, err, "direct SetOAuth keeps PAT (separate keys);/Login's storeOAuth clears it)")
	// Set PAT should keep OAuth
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_new"))
	_, err = keyring.GetOAuth("api.zetic.ai")
	assert.NoError(t, err, "direct Set keeps OAuth (separate keys)")
}

func TestEmptyAPIKeyFileDoesNotBlockOAuth(t *testing.T) {
	gokeyring.MockInit()
	t.Setenv(config.EnvAPIKey, "")
	dir := t.TempDir()
	emptyFile := filepath.Join(dir, "empty.txt")
	require.NoError(t, os.WriteFile(emptyFile, []byte(""), 0600))
	t.Setenv(config.EnvAPIKeyFile, emptyFile)
	creds := config.OAuthCredentials{AccessToken: "zoa_test", RefreshToken: "zor_test", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", creds))
	cfg := &config.Config{}
	// ResolveAny should use OAuth despite empty file
	res, oauthCreds, err := cfg.ResolveAnyTokenWith("api.zetic.ai", keyring.Lookup, keyring.LookupOAuth)
	require.NoError(t, err)
	assert.Equal(t, "zoa_test", res.Value)
	assert.NotNil(t, oauthCreds)
	// ResolveTokenWith should still return empty (legacy)
	res2, err := cfg.ResolveTokenWith("api.zetic.ai", keyring.Lookup)
	require.NoError(t, err)
	assert.Equal(t, "", res2.Value)
}
