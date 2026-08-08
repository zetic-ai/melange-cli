package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gokeyring "github.com/zalando/go-keyring"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
)

func TestResolveOAuthStorageConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	gokeyring.MockInit()
	creds := config.OAuthCredentials{AccessToken: "zoa_cfg", RefreshToken: "zor_cfg", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}
	cfg := &config.Config{}
	require.NoError(t, cfg.SetHostOAuth("api.zetic.ai", creds))
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", config.OAuthCredentials{AccessToken: "zoa_keyring", RefreshToken: "zor", Expiry: time.Now().Add(time.Hour), ClientID: "cid2"}))
	loaded, err := config.Load()
	require.NoError(t, err)
	got, src, err := loaded.ResolveOAuth("api.zetic.ai", keyring.LookupOAuth)
	require.NoError(t, err)
	assert.Equal(t, "zoa_cfg", got.AccessToken)
	assert.Equal(t, "oauth(config)", src)
}

func TestResolveOAuthKeyringError(t *testing.T) {
	cfg := &config.Config{}
	lookupErr := func(string) (*config.OAuthCredentials, bool, error) {
		return nil, false, errors.New("keyring locked")
	}
	_, _, err := cfg.ResolveOAuth("api.zetic.ai", lookupErr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving oauth from keyring")
	_, _, err = cfg.ResolveAnyTokenWith("api.zetic.ai", keyring.Lookup, lookupErr)
	require.Error(t, err)
}

func TestResolveOAuthLegacyFallback(t *testing.T) {
	gokeyring.MockInit()
	creds := config.OAuthCredentials{AccessToken: "zoa_legacy", RefreshToken: "zor_legacy", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}
	cfg := &config.Config{
		Hosts: map[string]config.HostEntry{
			"api.zetic.ai": {OAuth: &creds},
		},
	}
	got, src, err := cfg.ResolveOAuth("api.zetic.ai", keyring.LookupOAuth)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "zoa_legacy", got.AccessToken)
	assert.Equal(t, "oauth(config)", src)
	var nilCfg *config.Config
	got2, _, err := nilCfg.ResolveOAuth("api.zetic.ai", nil)
	require.NoError(t, err)
	assert.Nil(t, got2)
}

func TestResolveAnyTokenWithKeyringError(t *testing.T) {
	t.Setenv(config.EnvAPIKey, "")
	t.Setenv(config.EnvAPIKeyFile, "")
	gokeyring.MockInit()
	cfg := &config.Config{}
	lookupErr := func(string) (*config.OAuthCredentials, bool, error) {
		return nil, false, errors.New("locked")
	}
	creds := config.OAuthCredentials{AccessToken: "zoa_x", RefreshToken: "zor_x", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}
	cfg.Hosts = map[string]config.HostEntry{"api.zetic.ai": {OAuth: &creds, Storage: config.CredentialStorageConfig}}
	cfg2 := &config.Config{}
	_, _, err := cfg2.ResolveAnyTokenWith("api.zetic.ai", keyring.Lookup, lookupErr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving oauth")
	_, _, err = cfg2.ResolveOAuth("api.zetic.ai", lookupErr)
	require.Error(t, err)
	_, _, err = cfg.ResolveOAuth("api.zetic.ai", lookupErr)
	require.NoError(t, err)
}

func TestResolveAnyTokenWithEmptyFileDebug(t *testing.T) {
	t.Setenv(config.EnvAPIKey, "")
	dir := t.TempDir()
	emptyFile := filepath.Join(dir, "empty.txt")
	require.NoError(t, os.WriteFile(emptyFile, []byte("   \n"), 0600))
	t.Setenv(config.EnvAPIKeyFile, emptyFile)
	t.Setenv("MELANGE_DEBUG", "1")
	cfg := &config.Config{}
	res, creds, err := cfg.ResolveAnyTokenWith("api.zetic.ai", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "", res.Value)
	assert.Nil(t, creds)
}

func TestResolveAnyTokenWithUnreadableFile(t *testing.T) {
	t.Setenv(config.EnvAPIKey, "")
	t.Setenv(config.EnvAPIKeyFile, "/nonexistent/path/that/does/not/exist")
	cfg := &config.Config{}
	_, _, err := cfg.ResolveAnyTokenWith("api.zetic.ai", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading MELANGE_API_KEY_FILE")
}

func TestResolveAnyTokenWithStaleReturnsCredsForRefresh(t *testing.T) {
	t.Setenv(config.EnvAPIKey, "")
	t.Setenv(config.EnvAPIKeyFile, "")
	gokeyring.MockInit()
	stale := config.OAuthCredentials{AccessToken: "zoa_stale", RefreshToken: "zor_stale", Expiry: time.Now().Add(-time.Hour), ClientID: "cid"}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", stale))
	cfg := &config.Config{}
	res, creds, err := cfg.ResolveAnyTokenWith("api.zetic.ai", keyring.Lookup, keyring.LookupOAuth)
	require.NoError(t, err)
	assert.Equal(t, "", res.Value)
	require.NotNil(t, creds)
	assert.Equal(t, "zoa_stale", creds.AccessToken)
}

func TestResolveAnyTokenWithZeroExpiryFallsThroughToPAT(t *testing.T) {
	t.Setenv(config.EnvAPIKey, "")
	t.Setenv(config.EnvAPIKeyFile, "")
	gokeyring.MockInit()
	zero := config.OAuthCredentials{AccessToken: "zoa_zero", RefreshToken: "zor_zero", ClientID: "cid"}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", zero))
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_pat"))
	cfg := &config.Config{}
	res, creds, err := cfg.ResolveAnyTokenWith("api.zetic.ai", keyring.Lookup, keyring.LookupOAuth)
	require.NoError(t, err)
	assert.Equal(t, "ztp_pat", res.Value)
	assert.Nil(t, creds)
}

func TestConfigDirExtra(t *testing.T) {
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	origAppData := os.Getenv("APPDATA")
	origHome := os.Getenv("HOME")
	defer func() {
		t.Setenv("XDG_CONFIG_HOME", origXDG)
		t.Setenv("APPDATA", origAppData)
		t.Setenv("HOME", origHome)
	}()
	if runtime.GOOS == "windows" {
		dir := t.TempDir()
		t.Setenv("APPDATA", dir)
		expected := filepath.Join(dir, "melange")
		assert.Equal(t, expected, config.ConfigDir())
		t.Setenv("APPDATA", "")
		t.Setenv("XDG_CONFIG_HOME", dir)
		assert.Equal(t, filepath.Join(dir, "melange"), config.ConfigDir())
	} else {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		assert.Equal(t, filepath.Join(dir, "melange"), config.ConfigDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		assert.Equal(t, filepath.Join(home, ".config", "melange"), config.ConfigDir())
	}
}

func TestDeleteHostOAuthEdge(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	gokeyring.MockInit()
	cfg, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, cfg.DeleteHostOAuth("nonexistent"))
	creds := config.OAuthCredentials{AccessToken: "zoa_x", RefreshToken: "zor_x", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}
	cfg.Hosts = map[string]config.HostEntry{"api.zetic.ai": {OAuth: &creds, APIKey: "ztp_keep"}}
	require.NoError(t, config.Save(cfg))
	loaded, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, loaded.DeleteHostOAuth("api.zetic.ai"))
	entry := loaded.Hosts["api.zetic.ai"]
	assert.Equal(t, "ztp_keep", entry.APIKey)
	assert.Nil(t, entry.OAuth)
	require.NoError(t, loaded.SetHostOAuth("api.zetic.ai", creds))
	loaded2, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, loaded2.DeleteHostOAuth("api.zetic.ai"))
	_, ok := loaded2.Hosts["api.zetic.ai"]
	assert.False(t, ok)
}

func TestSaveToAndLoadFrom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "config.yml")
	cfg := &config.Config{Host: "https://api.zetic.ai", Hosts: map[string]config.HostEntry{"api.zetic.ai": {APIKey: "ztp_test", Storage: config.CredentialStorageConfig}}}
	require.NoError(t, config.SaveTo(cfg, path))
	loaded, err := config.LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "https://api.zetic.ai", loaded.Host)
	loaded2, err := config.LoadFrom(filepath.Join(dir, "nonexistent.yml"))
	require.NoError(t, err)
	assert.NotNil(t, loaded2)
	corrupt := filepath.Join(dir, "corrupt.yml")
	require.NoError(t, os.WriteFile(corrupt, []byte("::: not yaml ::::"), 0600))
	_, err = config.LoadFrom(corrupt)
	require.Error(t, err)
}

func TestSetHostAPIKeyClearsOAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	gokeyring.MockInit()
	cfg, err := config.Load()
	require.NoError(t, err)
	creds := config.OAuthCredentials{AccessToken: "zoa_x", RefreshToken: "zor_x", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}
	require.NoError(t, cfg.SetHostOAuth("api.zetic.ai", creds))
	loaded, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded.Hosts["api.zetic.ai"].OAuth)
	require.NoError(t, loaded.SetHostAPIKey("api.zetic.ai", "ztp_new"))
	loaded2, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "ztp_new", loaded2.Hosts["api.zetic.ai"].APIKey)
	assert.Nil(t, loaded2.Hosts["api.zetic.ai"].OAuth)
}

func TestDeleteHostAPIKeyKeepsOAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	gokeyring.MockInit()
	cfg, err := config.Load()
	require.NoError(t, err)
	creds := config.OAuthCredentials{AccessToken: "zoa_x", RefreshToken: "zor_x", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}
	require.NoError(t, cfg.SetHostOAuth("api.zetic.ai", creds))
	loaded, err := config.Load()
	require.NoError(t, err)
	entry := loaded.Hosts["api.zetic.ai"]
	entry.APIKey = "ztp_pat"
	loaded.Hosts["api.zetic.ai"] = entry
	require.NoError(t, config.Save(loaded))
	loaded2, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, loaded2.DeleteHostAPIKey("api.zetic.ai"))
	assert.NotNil(t, loaded2.Hosts["api.zetic.ai"].OAuth)
	assert.Equal(t, "", loaded2.Hosts["api.zetic.ai"].APIKey)
}
