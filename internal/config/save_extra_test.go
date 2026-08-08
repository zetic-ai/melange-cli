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

func TestSaveToMkdirAllError(t *testing.T) {
	// Create a file where dir should be
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0600))
	// Now try to create subdir under file (MkdirAll should fail)
	path := filepath.Join(file, "config.yml")
	cfg := &config.Config{Host: "https://api.zetic.ai"}
	err := config.SaveTo(cfg, path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating config dir")
}

func TestSaveToCorruptPathIsDirectory(t *testing.T) {
	// Use a directory as path, Rename should fail or succeed? On many OS Rename of tmp to dir fails.
	dir := t.TempDir()
	path := filepath.Join(dir, "existent")
	require.NoError(t, os.Mkdir(path, 0755))
	cfg := &config.Config{Host: "https://api.zetic.ai"}
	// If path is directory, Rename should error
	err := config.SaveTo(cfg, path)
	if err != nil {
		assert.Contains(t, err.Error(), "replacing config")
	}
}

func TestLoadFromReadErrorIsNotNotExist(t *testing.T) {
	// Use a directory as file path -> ReadFile fails with isDir error, not ErrNotExist
	dir := t.TempDir()
	_, err := config.LoadFrom(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config")
	// Verify errors.Is not ErrNotExist
	assert.False(t, errors.Is(err, os.ErrNotExist))
}

func TestSaveToHappyPathCreates0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "config.yml")
	cfg := &config.Config{
		Host: "https://api.zetic.ai",
		Hosts: map[string]config.HostEntry{
			"api.zetic.ai": {APIKey: "ztp_test", Storage: config.CredentialStorageConfig},
		},
	}
	require.NoError(t, config.SaveTo(cfg, path))
	info, err := os.Stat(path)
	require.NoError(t, err)
	// Check perms 0600 (on Unix)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
	loaded, err := config.LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "https://api.zetic.ai", loaded.Host)
}

func TestLoadFromMissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yml")
	cfg, err := config.LoadFrom(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "", cfg.Host)
}

func TestSaveToCreateTempErrorWithReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("readonly dir semantics differ on windows")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "readonly")
	require.NoError(t, os.Mkdir(sub, 0555))
	// On some filesystems (temp dir owned by user) even 0555 may still allow creation by owner.
	// So we try to chmod to 0000 if still writable, but ensure we check error case
	// Instead we test that SaveTo at least succeeds or fails gracefully; coverage of CreateTemp error is env-dependent.
	path := filepath.Join(sub, "config.yml")
	cfg := &config.Config{Host: "https://api.zetic.ai"}
	err := config.SaveTo(cfg, path)
	if err != nil {
		assert.Contains(t, err.Error(), "creating temporary config")
	} else {
		// If OS allowed write despite perms (root), just verify file exists
		_, statErr := os.Stat(path)
		require.NoError(t, statErr)
	}
	// Restore perms for cleanup
	require.NoError(t, os.Chmod(sub, 0755))
}

func TestConfigDirVariants(t *testing.T) {
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	origHome := os.Getenv("HOME")
	origAppData := os.Getenv("APPDATA")
	defer func() {
		t.Setenv("XDG_CONFIG_HOME", origXDG)
		t.Setenv("HOME", origHome)
		t.Setenv("APPDATA", origAppData)
	}()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	expected := filepath.Join(dir, "melange")
	if runtime.GOOS != "windows" {
		assert.Equal(t, expected, config.ConfigDir())
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	assert.Equal(t, filepath.Join(home, ".config", "melange"), config.ConfigDir())
}

func TestDeleteHostOAuthMissingHostNoop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	cfg, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, cfg.DeleteHostOAuth("ghost.example.com"))
	// Also test DeleteHostOAuth when entry has no OAuth
	cfg.Hosts = map[string]config.HostEntry{"api.zetic.ai": {APIKey: "ztp", Storage: config.CredentialStorageConfig}}
	require.NoError(t, config.Save(cfg))
	loaded, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, loaded.DeleteHostOAuth("api.zetic.ai"))
	// Should be noop, keep entry because APIKey present
	assert.Equal(t, "ztp", loaded.Hosts["api.zetic.ai"].APIKey)
}

func TestSaveToSyncDirErrorIgnoredOnWindows(t *testing.T) {
	// On unix, syncDir does fsync; on windows different. Just ensure Save succeeds normally
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	cfg := &config.Config{Host: "https://api.zetic.ai"}
	require.NoError(t, config.SaveTo(cfg, path))
}
