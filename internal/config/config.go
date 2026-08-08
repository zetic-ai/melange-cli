// Package config handles loading, saving, and resolving configuration values
// for the melange CLI. Credential resolution follows this order:
// environment > environment file > explicitly selected config storage >
// keyring > legacy config fallback.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Env variable names — centralized so callers don't hard-code strings.
const (
	EnvHost       = "MELANGE_HOST"
	EnvAPIKey     = "MELANGE_API_KEY"
	EnvAPIKeyFile = "MELANGE_API_KEY_FILE"

	DefaultHost = "https://api.zetic.ai"

	// CredentialStorageConfig marks a host whose token was explicitly stored
	// in the mode-0600 config file after the user opted into
	// --insecure-storage. Resolution skips the unavailable keyring only for
	// this explicit per-host selection.
	CredentialStorageConfig = "config"
)

// OAuthCredentials holds OAuth token pair for a host.
type OAuthCredentials struct {
	AccessToken  string    `yaml:"access_token"`
	RefreshToken string    `yaml:"refresh_token"`
	Expiry       time.Time `yaml:"expiry,omitempty"`
	ClientID     string    `yaml:"client_id"`
	Scope        string    `yaml:"scope,omitempty"`
	TokenType    string    `yaml:"token_type,omitempty"`
}

// HostEntry holds per-host credentials.
type HostEntry struct {
	APIKey  string            `yaml:"api_key"`
	Storage string            `yaml:"storage,omitempty"`
	OAuth   *OAuthCredentials `yaml:"oauth,omitempty"`
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

// ResolveToken returns the API token for the given host without consulting a
// keyring. Call ResolveTokenWith for the full CLI credential resolution path.
// A set-but-unreadable MELANGE_API_KEY_FILE is a hard error.
func (c *Config) ResolveToken(host string) (Resolved, error) {
	return c.ResolveTokenWith(host, nil)
}

// ResolveTokenWith is ResolveToken with an injected keyring lookup (nil = no
// keyring). The lookup is a plain func so this package does not import
// internal/keyring; commands pass keyring.Lookup. Precedence:
//
//	env:MELANGE_API_KEY > env:MELANGE_API_KEY_FILE > explicitly selected
//	config storage > keyring > legacy config > empty
//
// A keyring lookup failure remains a hard error unless this host explicitly
// selected config storage. This prevents an unavailable keyring from silently
// switching credentials.
func (c *Config) ResolveTokenWith(host string, lookup func(host string) (string, bool, error)) (Resolved, error) {
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
	var configured HostEntry
	if c != nil && c.Hosts != nil {
		configured = c.Hosts[host]
	}
	if configured.Storage == CredentialStorageConfig && configured.APIKey != "" {
		return Resolved{Value: configured.APIKey, Source: "config"}, nil
	}
	if lookup != nil {
		v, ok, err := lookup(host)
		if err != nil {
			return Resolved{}, fmt.Errorf("resolving token from keyring: %w", err)
		}
		if ok && v != "" {
			return Resolved{Value: v, Source: "keyring"}, nil
		}
	}
	if configured.APIKey != "" {
		return Resolved{Value: configured.APIKey, Source: "config"}, nil
	}
	return Resolved{}, nil
}

// SetHostAPIKey stores an API key for host in the config file and saves it.
func (c *Config) SetHostAPIKey(host, key string) error {
	if c.Hosts == nil {
		c.Hosts = make(map[string]HostEntry)
	}
	// Clear OAuth for this host (mutually exclusive).
	c.Hosts[host] = HostEntry{APIKey: key, Storage: CredentialStorageConfig}
	return Save(c)
}

// DeleteHostAPIKey removes the API key for host from the config file and
// saves it. Removing an absent host is not an error.
func (c *Config) DeleteHostAPIKey(host string) error {
	entry, ok := c.Hosts[host]
	if !ok {
		return nil
	}
	entry.APIKey = ""
	if entry.OAuth == nil {
		delete(c.Hosts, host)
	} else {
		c.Hosts[host] = entry
	}
	return Save(c)
}

// SetHostOAuth stores OAuth credentials for host, clearing any PAT.
func (c *Config) SetHostOAuth(host string, creds OAuthCredentials) error {
	if c.Hosts == nil {
		c.Hosts = make(map[string]HostEntry)
	}
	cc := creds
	c.Hosts[host] = HostEntry{OAuth: &cc, Storage: CredentialStorageConfig}
	return Save(c)
}

// DeleteHostOAuth removes OAuth credentials for host.
func (c *Config) DeleteHostOAuth(host string) error {
	entry, ok := c.Hosts[host]
	if !ok {
		return nil
	}
	if entry.OAuth == nil {
		return nil
	}
	entry.OAuth = nil
	if entry.APIKey == "" && entry.OAuth == nil {
		delete(c.Hosts, host)
	} else {
		c.Hosts[host] = entry
	}
	return Save(c)
}

// ResolveOAuth returns OAuth credentials for host, checking config vs keyring.
func (c *Config) ResolveOAuth(host string, lookup func(string) (*OAuthCredentials, bool, error)) (*OAuthCredentials, string, error) {
	var configured HostEntry
	if c != nil && c.Hosts != nil {
		configured = c.Hosts[host]
	}
	if configured.Storage == CredentialStorageConfig && configured.OAuth != nil {
		return configured.OAuth, "oauth(config)", nil
	}
	if lookup != nil {
		creds, ok, err := lookup(host)
		if err != nil {
			return nil, "", fmt.Errorf("resolving oauth from keyring: %w", err)
		}
		if ok && creds != nil {
			return creds, "oauth(keyring)", nil
		}
	}
	if configured.OAuth != nil {
		return configured.OAuth, "oauth(config)", nil
	}
	return nil, "", nil
}

// ResolveAnyTokenWith resolves the effective token with precedence:
// MELANGE_API_KEY > MELANGE_API_KEY_FILE (non-empty) > OAuth(fresh) > PAT keyring > PAT config.
// Empty MELANGE_API_KEY_FILE is treated as unset for OAuth path (falls through) but ResolveTokenWith preserves empty behavior.
func (c *Config) ResolveAnyTokenWith(host string, lookupPAT func(string) (string, bool, error), lookupOAuth func(string) (*OAuthCredentials, bool, error)) (Resolved, *OAuthCredentials, error) {
	if v := os.Getenv(EnvAPIKey); v != "" {
		return Resolved{Value: v, Source: "env:" + EnvAPIKey}, nil, nil
	}
	if path := os.Getenv(EnvAPIKeyFile); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Resolved{}, nil, fmt.Errorf("reading %s (%s): %w", EnvAPIKeyFile, path, err)
		}
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" {
			if os.Getenv("MELANGE_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "! %s empty, ignoring\n", EnvAPIKeyFile)
			}
		} else {
			return Resolved{Value: trimmed, Source: "env:" + EnvAPIKeyFile}, nil, nil
		}
	}
	// OAuth fresh check
	if lookupOAuth != nil || (c != nil && c.Hosts != nil && c.Hosts[host].OAuth != nil) {
		creds, src, err := c.ResolveOAuth(host, lookupOAuth)
		if err != nil {
			return Resolved{}, nil, err
		}
		if creds != nil {
			if creds.Expiry.IsZero() {
				// Treat zero expiry as absent per spec; fall through to PAT.
			} else if creds.Expiry.After(time.Now().Add(30 * time.Second)) {
				return Resolved{Value: creds.AccessToken, Source: src}, creds, nil
			} else {
				// Stale — return creds so caller can refresh instead of falling back to PAT.
				return Resolved{}, creds, nil
			}
		}
	}
	// Fall back to PAT
	res, err := c.ResolveTokenWith(host, lookupPAT)
	if err != nil {
		return Resolved{}, nil, err
	}
	if res.Value != "" {
		return res, nil, nil
	}
	return Resolved{}, nil, nil
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
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("securing temporary config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("writing temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("syncing temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replacing config: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("syncing config directory: %w", err)
	}
	return nil
}
