package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/config"
)

// ---------------------------------------------------------------------------
// Host precedence tests
// ---------------------------------------------------------------------------

func TestResolveHost(t *testing.T) {
	tests := []struct {
		name       string
		flagValue  string
		envValue   string // MELANGE_HOST
		cfgValue   string
		wantValue  string
		wantSource string
	}{
		{
			name:       "flag wins over env",
			flagValue:  "https://flag.example.com",
			envValue:   "https://env.example.com",
			cfgValue:   "https://cfg.example.com",
			wantValue:  "https://flag.example.com",
			wantSource: "flag",
		},
		{
			name:       "env wins over config",
			flagValue:  "",
			envValue:   "https://env.example.com",
			cfgValue:   "https://cfg.example.com",
			wantValue:  "https://env.example.com",
			wantSource: "env:MELANGE_HOST",
		},
		{
			name:       "config wins over default",
			flagValue:  "",
			envValue:   "",
			cfgValue:   "https://cfg.example.com",
			wantValue:  "https://cfg.example.com",
			wantSource: "config",
		},
		{
			name:       "default when nothing set",
			flagValue:  "",
			envValue:   "",
			cfgValue:   "",
			wantValue:  config.DefaultHost,
			wantSource: "default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv(config.EnvHost, tt.envValue)
			} else {
				t.Setenv(config.EnvHost, "")
			}

			cfg := &config.Config{}
			if tt.cfgValue != "" {
				cfg.Host = tt.cfgValue
			}

			got := cfg.ResolveHost(tt.flagValue)
			assert.Equal(t, tt.wantValue, got.Value)
			assert.Equal(t, tt.wantSource, got.Source)
		})
	}
}

// ---------------------------------------------------------------------------
// Token precedence tests
// ---------------------------------------------------------------------------

func TestResolveToken(t *testing.T) {
	t.Run("env:MELANGE_API_KEY beats config", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "env-key")
		t.Setenv(config.EnvAPIKeyFile, "")

		cfg := &config.Config{
			Hosts: map[string]config.HostEntry{
				"api.zetic.ai": {APIKey: "cfg-key"},
			},
		}
		got, err := cfg.ResolveToken("api.zetic.ai")
		require.NoError(t, err)
		assert.Equal(t, "env-key", got.Value)
		assert.Equal(t, "env:MELANGE_API_KEY", got.Source)
	})

	t.Run("env:MELANGE_API_KEY_FILE beats config", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")

		// Write a key file with trailing newline
		dir := t.TempDir()
		keyFile := filepath.Join(dir, "key.txt")
		require.NoError(t, os.WriteFile(keyFile, []byte("file-key\n"), 0600))
		t.Setenv(config.EnvAPIKeyFile, keyFile)

		cfg := &config.Config{
			Hosts: map[string]config.HostEntry{
				"api.zetic.ai": {APIKey: "cfg-key"},
			},
		}
		got, err := cfg.ResolveToken("api.zetic.ai")
		require.NoError(t, err)
		assert.Equal(t, "file-key", got.Value) // trailing newline trimmed
		assert.Equal(t, "env:MELANGE_API_KEY_FILE", got.Source)
	})

	t.Run("env:MELANGE_API_KEY_FILE set but unreadable is a hard error", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		missing := filepath.Join(t.TempDir(), "does-not-exist.txt")
		t.Setenv(config.EnvAPIKeyFile, missing)

		cfg := &config.Config{
			Hosts: map[string]config.HostEntry{
				"api.zetic.ai": {APIKey: "cfg-key"},
			},
		}
		// Never silently fall through to keyring/config: an operator who set
		// MELANGE_API_KEY_FILE must not end up on a different credential.
		got, err := cfg.ResolveToken("api.zetic.ai")
		require.Error(t, err)
		assert.Contains(t, err.Error(), missing, "error must name the unreadable path")
		assert.Contains(t, err.Error(), config.EnvAPIKeyFile)
		assert.Equal(t, "", got.Value)
	})

	t.Run("env:MELANGE_API_KEY_FILE readable but empty short-circuits without error", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		dir := t.TempDir()
		keyFile := filepath.Join(dir, "empty.txt")
		require.NoError(t, os.WriteFile(keyFile, []byte(""), 0600))
		t.Setenv(config.EnvAPIKeyFile, keyFile)

		cfg := &config.Config{
			Hosts: map[string]config.HostEntry{
				"api.zetic.ai": {APIKey: "cfg-key"},
			},
		}
		got, err := cfg.ResolveToken("api.zetic.ai")
		require.NoError(t, err)
		assert.Equal(t, "", got.Value, "empty readable file keeps the not-logged-in short-circuit")
		assert.Equal(t, "env:"+config.EnvAPIKeyFile, got.Source)
	})

	t.Run("file key with trailing whitespace is trimmed", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")

		dir := t.TempDir()
		keyFile := filepath.Join(dir, "key.txt")
		require.NoError(t, os.WriteFile(keyFile, []byte("  key-with-spaces  \n"), 0600))
		t.Setenv(config.EnvAPIKeyFile, keyFile)

		cfg := &config.Config{}
		got, err := cfg.ResolveToken("api.zetic.ai")
		require.NoError(t, err)
		assert.Equal(t, "key-with-spaces", got.Value)
		assert.Equal(t, "env:MELANGE_API_KEY_FILE", got.Source)
	})

	t.Run("config wins over empty", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		t.Setenv(config.EnvAPIKeyFile, "")

		cfg := &config.Config{
			Hosts: map[string]config.HostEntry{
				"api.zetic.ai": {APIKey: "cfg-key"},
			},
		}
		got, err := cfg.ResolveToken("api.zetic.ai")
		require.NoError(t, err)
		assert.Equal(t, "cfg-key", got.Value)
		assert.Equal(t, "config", got.Source)
	})

	t.Run("empty when nothing set", func(t *testing.T) {
		t.Setenv(config.EnvAPIKey, "")
		t.Setenv(config.EnvAPIKeyFile, "")

		cfg := &config.Config{}
		got, err := cfg.ResolveToken("api.zetic.ai")
		require.NoError(t, err)
		assert.Equal(t, "", got.Value)
		assert.Equal(t, "", got.Source)
	})
}

// ---------------------------------------------------------------------------
// Load / Save tests
// ---------------------------------------------------------------------------

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent", "config.yml")

	cfg, err := config.LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Host)
	assert.Equal(t, "", cfg.DefaultRepo)
}

func TestLoadCorruptedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte("{ bad yaml: ["), 0600))

	_, err := config.LoadFrom(path)
	assert.Error(t, err)
}

func TestLoadAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")

	cfg := &config.Config{
		Host:        "https://custom.example.com",
		DefaultRepo: "my-repo",
		Hosts: map[string]config.HostEntry{
			"custom.example.com": {APIKey: "secret"},
		},
	}

	require.NoError(t, config.SaveTo(cfg, path))

	// Verify file permissions
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}

	// Load and verify round-trip
	loaded, err := config.LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, cfg.Host, loaded.Host)
	assert.Equal(t, cfg.DefaultRepo, loaded.DefaultRepo)
	assert.Equal(t, "secret", loaded.Hosts["custom.example.com"].APIKey)
}

func TestSaveTightensExistingConfigPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	require.NoError(t, os.WriteFile(path, []byte("host: old\n"), 0644))
	require.NoError(t, os.Chmod(path, 0644))

	before, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, config.SaveTo(&config.Config{Host: "https://api.zetic.ai"}, path))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.False(t, os.SameFile(before, info),
		"config saves must atomically replace instead of truncating in place")
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(),
		"rewriting an old config must not preserve permissive modes")
}

func TestConfigDir(t *testing.T) {
	dir := config.ConfigDir()
	assert.NotEmpty(t, dir)
	assert.Contains(t, dir, "melange")
}
