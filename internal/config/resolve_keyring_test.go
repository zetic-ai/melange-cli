package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/config"
)

// staticLookup returns a keyring-style lookup func backed by a map.
func staticLookup(m map[string]string) func(host string) (string, bool, error) {
	return func(host string) (string, bool, error) {
		v, ok := m[host]
		return v, ok, nil
	}
}

func TestResolveTokenWith(t *testing.T) {
	const host = "api.zetic.ai"

	t.Run("env:MELANGE_API_KEY beats keyring", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "env-key")
		t.Setenv(config.EnvAPIKeyFile, "")

		cfg := &config.Config{}
		got, err := cfg.ResolveTokenWith(host, staticLookup(map[string]string{host: "keyring-key"}))
		require.NoError(t, err)
		assert.Equal(t, "env-key", got.Value)
		assert.Equal(t, "env:MELANGE_API_KEY", got.Source)
	})

	t.Run("env:MELANGE_API_KEY_FILE beats keyring", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		dir := t.TempDir()
		keyFile := filepath.Join(dir, "key.txt")
		require.NoError(t, os.WriteFile(keyFile, []byte("file-key\n"), 0600))
		t.Setenv(config.EnvAPIKeyFile, keyFile)

		cfg := &config.Config{}
		got, err := cfg.ResolveTokenWith(host, staticLookup(map[string]string{host: "keyring-key"}))
		require.NoError(t, err)
		assert.Equal(t, "file-key", got.Value)
		assert.Equal(t, "env:MELANGE_API_KEY_FILE", got.Source)
	})

	t.Run("keyring beats config", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		t.Setenv(config.EnvAPIKeyFile, "")

		cfg := &config.Config{
			Hosts: map[string]config.HostEntry{host: {APIKey: "cfg-key"}},
		}
		got, err := cfg.ResolveTokenWith(host, staticLookup(map[string]string{host: "keyring-key"}))
		require.NoError(t, err)
		assert.Equal(t, "keyring-key", got.Value)
		assert.Equal(t, "keyring", got.Source)
	})

	t.Run("config when keyring misses", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		t.Setenv(config.EnvAPIKeyFile, "")

		cfg := &config.Config{
			Hosts: map[string]config.HostEntry{host: {APIKey: "cfg-key"}},
		}
		got, err := cfg.ResolveTokenWith(host, staticLookup(nil))
		require.NoError(t, err)
		assert.Equal(t, "cfg-key", got.Value)
		assert.Equal(t, "config", got.Source)
	})

	t.Run("keyring failure never falls through to config", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		t.Setenv(config.EnvAPIKeyFile, "")
		locked := errors.New("keychain locked")
		cfg := &config.Config{
			Hosts: map[string]config.HostEntry{host: {APIKey: "cfg-key"}},
		}
		got, err := cfg.ResolveTokenWith(host, func(string) (string, bool, error) {
			return "", false, locked
		})
		require.ErrorIs(t, err, locked)
		assert.Empty(t, got)
	})

	t.Run("explicit config storage bypasses unavailable keyring", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		t.Setenv(config.EnvAPIKeyFile, "")
		called := false
		cfg := &config.Config{
			Hosts: map[string]config.HostEntry{
				host: {APIKey: "cfg-key", Storage: config.CredentialStorageConfig},
			},
		}
		got, err := cfg.ResolveTokenWith(host, func(string) (string, bool, error) {
			called = true
			return "", false, errors.New("keychain locked")
		})
		require.NoError(t, err)
		assert.Equal(t, "cfg-key", got.Value)
		assert.Equal(t, "config", got.Source)
		assert.False(t, called, "an explicitly selected config credential must not query keyring")
	})

	t.Run("empty when nothing set", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		t.Setenv(config.EnvAPIKeyFile, "")

		cfg := &config.Config{}
		got, err := cfg.ResolveTokenWith(host, staticLookup(nil))
		require.NoError(t, err)
		assert.Equal(t, "", got.Value)
		assert.Equal(t, "", got.Source)
	})

	t.Run("nil lookup behaves like ResolveToken", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		t.Setenv(config.EnvAPIKeyFile, "")

		cfg := &config.Config{
			Hosts: map[string]config.HostEntry{host: {APIKey: "cfg-key"}},
		}
		got, err := cfg.ResolveTokenWith(host, nil)
		require.NoError(t, err)
		assert.Equal(t, "cfg-key", got.Value)
		assert.Equal(t, "config", got.Source)
	})
}

func TestSetAndDeleteHostAPIKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)

	cfg := &config.Config{}
	require.NoError(t, cfg.SetHostAPIKey("api.zetic.ai", "ztp_secret"))

	// Persisted to disk with 0600 perms.
	path := filepath.Join(dir, "melange", "config.yml")
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}

	loaded, err := config.LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "ztp_secret", loaded.Hosts["api.zetic.ai"].APIKey)
	assert.Equal(t, config.CredentialStorageConfig, loaded.Hosts["api.zetic.ai"].Storage)

	require.NoError(t, cfg.DeleteHostAPIKey("api.zetic.ai"))
	loaded, err = config.LoadFrom(path)
	require.NoError(t, err)
	_, ok := loaded.Hosts["api.zetic.ai"]
	assert.False(t, ok, "host entry should be removed")
}

func TestDeleteHostAPIKeyMissingIsNoError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)

	cfg := &config.Config{}
	assert.NoError(t, cfg.DeleteHostAPIKey("absent.example.com"))
}
