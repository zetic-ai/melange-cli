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

func TestSetHostOAuthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	gokeyring.MockInit()
	cfg, err := config.Load()
	require.NoError(t, err)
	creds := config.OAuthCredentials{
		AccessToken:  "zoa_test",
		RefreshToken: "zor_test",
		Expiry:       time.Now().Add(time.Hour).Truncate(time.Second),
		ClientID:     "client123",
		Scope:        "write",
		TokenType:    "Bearer",
	}
	require.NoError(t, cfg.SetHostOAuth("api.zetic.ai", creds))
	loaded, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded.Hosts["api.zetic.ai"].OAuth)
	assert.Equal(t, creds.AccessToken, loaded.Hosts["api.zetic.ai"].OAuth.AccessToken)
	assert.Equal(t, creds.RefreshToken, loaded.Hosts["api.zetic.ai"].OAuth.RefreshToken)
	assert.Equal(t, creds.ClientID, loaded.Hosts["api.zetic.ai"].OAuth.ClientID)
	assert.WithinDuration(t, creds.Expiry, loaded.Hosts["api.zetic.ai"].OAuth.Expiry, time.Second)
	configPath := filepath.Join(config.ConfigDir(), "config.yml")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "0001-01-01")
	emptyCfg := &config.Config{}
	dir2 := t.TempDir()
	path := filepath.Join(dir2, "config.yml")
	require.NoError(t, config.SaveTo(emptyCfg, path))
	emptyData, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(emptyData), "oauth")
}

func TestDeleteHostOAuthWhenConfigStorage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	gokeyring.MockInit()
	cfg, err := config.Load()
	require.NoError(t, err)
	creds := config.OAuthCredentials{
		AccessToken:  "zoa_test",
		RefreshToken: "zor_test",
		Expiry:       time.Now().Add(time.Hour),
		ClientID:     "cid",
	}
	require.NoError(t, cfg.SetHostOAuth("api.zetic.ai", creds))
	loaded, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, loaded.DeleteHostOAuth("api.zetic.ai"))
	_, ok := loaded.Hosts["api.zetic.ai"]
	assert.False(t, ok, "whole entry should be deleted when Storage==config and no APIKey")
}

func TestDeleteHostOAuthKeepsPAT(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	gokeyring.MockInit()
	cfg, err := config.Load()
	require.NoError(t, err)
	creds := config.OAuthCredentials{
		AccessToken:  "zoa_test",
		RefreshToken: "zor_test",
		Expiry:       time.Now().Add(time.Hour),
		ClientID:     "cid",
	}
	// First set PAT via config
	require.NoError(t, cfg.SetHostOAuth("api.zetic.ai", creds))
	// Manually set APIKey to simulate both PAT and OAuth
	loaded, err := config.Load()
	require.NoError(t, err)
	entry := loaded.Hosts["api.zetic.ai"]
	entry.APIKey = "ztp_pat"
	loaded.Hosts["api.zetic.ai"] = entry
	require.NoError(t, config.Save(loaded))
	// Now delete OAuth should keep PAT
	loaded2, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, loaded2.DeleteHostOAuth("api.zetic.ai"))
	entry2 := loaded2.Hosts["api.zetic.ai"]
	assert.Equal(t, "ztp_pat", entry2.APIKey)
	assert.Nil(t, entry2.OAuth)
}

func TestResolveAnyTokenWithOAuthFreshAndStale(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	gokeyring.MockInit()
	creds := config.OAuthCredentials{
		AccessToken:  "zoa_fresh",
		RefreshToken: "zor_fresh",
		Expiry:       time.Now().Add(time.Hour),
		ClientID:     "cid",
	}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", creds))
	cfg := &config.Config{}
	res, oauthCreds, err := cfg.ResolveAnyTokenWith("api.zetic.ai", keyring.Lookup, keyring.LookupOAuth)
	require.NoError(t, err)
	assert.Equal(t, "zoa_fresh", res.Value)
	assert.NotNil(t, oauthCreds)
	assert.Equal(t, "zoa_fresh", oauthCreds.AccessToken)

	stale := config.OAuthCredentials{
		AccessToken:  "zoa_stale",
		RefreshToken: "zor_stale",
		Expiry:       time.Now().Add(-time.Hour),
		ClientID:     "cid",
	}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", stale))
	res2, oauthCreds2, err := cfg.ResolveAnyTokenWith("api.zetic.ai", keyring.Lookup, keyring.LookupOAuth)
	require.NoError(t, err)
	assert.Equal(t, "", res2.Value)
	assert.NotNil(t, oauthCreds2)
	assert.Equal(t, "zoa_stale", oauthCreds2.AccessToken)

	zero := config.OAuthCredentials{
		AccessToken:  "zoa_zero",
		RefreshToken: "zor_zero",
		Expiry:       time.Time{},
		ClientID:     "cid",
	}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", zero))
	_ = keyring.Delete("api.zetic.ai")
	res3, oauthCreds3, err := cfg.ResolveAnyTokenWith("api.zetic.ai", keyring.Lookup, keyring.LookupOAuth)
	require.NoError(t, err)
	assert.Equal(t, "", res3.Value)
	assert.Nil(t, oauthCreds3, "zero expiry treated as absent")
}
