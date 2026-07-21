// Package config handles loading, saving, and resolving configuration values
// for the melange CLI. Configuration is stored in a YAML file; precedence
// resolution always wins in this order: flag > env > env-file > keyring >
// config > default.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// Env variable names — centralized so callers don't hard-code strings.
const (
	EnvHost       = "MELANGE_HOST"
	EnvAPIKey     = "MELANGE_API_KEY"
	EnvAPIKeyFile = "MELANGE_API_KEY_FILE"

	DefaultHost = "https://api.zetic.ai"
)

// HostEntry holds per-host credentials.
type HostEntry struct {
	APIKey string `yaml:"api_key"`
}

// Config is the top-level config file schema.
type Config struct {
	Host        string               `yaml:"host"`
	DefaultRepo string               `yaml:"default_repo"`
	Hosts       map[string]HostEntry `yaml:"hosts"`
}

// Resolved is a value together with its source label.
type Resolved struct {
	Value  string
	Source string
}

// ResolveHost returns the API host following the precedence chain:
//
//	flag > env:MELANGE_HOST > config.host > default
func (c *Config) ResolveHost(flagValue string) Resolved {
	if flagValue != "" {
		return Resolved{Value: flagValue, Source: "flag"}
	}
	if v := os.Getenv(EnvHost); v != "" {
		return Resolved{Value: v, Source: "env:" + EnvHost}
	}
	if c != nil && c.Host != "" {
		return Resolved{Value: c.Host, Source: "config"}
	}
	return Resolved{Value: DefaultHost, Source: "default"}
}

// ResolveToken returns the API token for the given host following the
// precedence chain:
//
//	env:MELANGE_API_KEY > env:MELANGE_API_KEY_FILE > config(hosts.<host>.api_key) > empty
//
// A set-but-unreadable MELANGE_API_KEY_FILE is a hard error: falling through
// to keyring/config would silently switch credentials.
func (c *Config) ResolveToken(host string) (Resolved, error) {
	return c.ResolveTokenWith(host, nil)
}

// ResolveTokenWith is ResolveToken with an injected keyring lookup (nil = no
// keyring). The lookup is a plain func so this package does not import
// internal/keyring; commands pass keyring.Lookup. Precedence:
//
//	env:MELANGE_API_KEY > env:MELANGE_API_KEY_FILE > keyring > config > empty
func (c *Config) ResolveTokenWith(host string, lookup func(host string) (string, bool)) (Resolved, error) {
	if v := os.Getenv(EnvAPIKey); v != "" {
		return Resolved{Value: v, Source: "env:" + EnvAPIKey}, nil
	}
	if path := os.Getenv(EnvAPIKeyFile); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			// The operator explicitly pointed at a key file; never fall
			// through to a different credential source on a read failure.
			return Resolved{}, fmt.Errorf("reading %s (%s): %w", EnvAPIKeyFile, path, err)
		}
		// A readable-but-empty file still short-circuits to "no token".
		return Resolved{
			Value:  strings.TrimSpace(string(raw)),
			Source: "env:" + EnvAPIKeyFile,
		}, nil
	}
	if lookup != nil {
		if v, ok := lookup(host); ok && v != "" {
			return Resolved{Value: v, Source: "keyring"}, nil
		}
	}
	if c != nil && c.Hosts != nil {
		if entry, ok := c.Hosts[host]; ok && entry.APIKey != "" {
			return Resolved{Value: entry.APIKey, Source: "config"}, nil
		}
	}
	return Resolved{}, nil
}

// SetHostAPIKey stores an API key for host in the config file and saves it.
func (c *Config) SetHostAPIKey(host, key string) error {
	if c.Hosts == nil {
		c.Hosts = make(map[string]HostEntry)
	}
	c.Hosts[host] = HostEntry{APIKey: key}
	return Save(c)
}

// DeleteHostAPIKey removes the API key for host from the config file and
// saves it. Removing an absent host is not an error.
func (c *Config) DeleteHostAPIKey(host string) error {
	if _, ok := c.Hosts[host]; !ok {
		return nil
	}
	delete(c.Hosts, host)
	return Save(c)
}

// ConfigDir returns the platform-appropriate directory for the config file.
//
//   - Linux/macOS: ${XDG_CONFIG_HOME:-$HOME/.config}/melange
//   - Windows:     %AppData%\melange
func ConfigDir() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "melange")
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "melange")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "melange")
}

// configPath returns the full path to the config file.
func configPath() string {
	return filepath.Join(ConfigDir(), "config.yml")
}

// Load loads the config from the default config path.
// A missing file is not an error — it returns an empty Config.
// A corrupted YAML file returns an error.
func Load() (*Config, error) {
	return LoadFrom(configPath())
}

// LoadFrom loads config from the specified path.
// A missing file returns an empty Config without error.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return &cfg, nil
}

// Save saves the config to the default config path (0600 file, 0755 dir).
func Save(cfg *Config) error {
	return SaveTo(cfg, configPath())
}

// SaveTo saves the config to the specified path.
func SaveTo(cfg *Config, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}
