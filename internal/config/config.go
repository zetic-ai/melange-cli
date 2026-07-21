// Package config handles loading, saving, and resolving configuration values
// for the melange CLI. Configuration is stored in a YAML file; precedence
// resolution always wins in this order: flag > env > env-file > config > default.
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
func (c *Config) ResolveToken(host string) Resolved {
	if v := os.Getenv(EnvAPIKey); v != "" {
		return Resolved{Value: v, Source: "env:" + EnvAPIKey}
	}
	if path := os.Getenv(EnvAPIKeyFile); path != "" {
		raw, err := os.ReadFile(path)
		if err == nil {
			return Resolved{
				Value:  strings.TrimSpace(string(raw)),
				Source: "env:" + EnvAPIKeyFile,
			}
		}
	}
	if c != nil && c.Hosts != nil {
		if entry, ok := c.Hosts[host]; ok && entry.APIKey != "" {
			return Resolved{Value: entry.APIKey, Source: "config"}
		}
	}
	return Resolved{}
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
