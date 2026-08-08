// Package keyring stores Melange credentials in the OS keychain via
// zalando/go-keyring. Entries live under the "melange-cli" service, keyed by
// host (scheme stripped, port kept — see HostKey).
package keyring

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	gokeyring "github.com/zalando/go-keyring"
	"github.com/zetic-ai/melange-cli/internal/config"
)

// service is the keychain service name for all melange credentials.
const service = "melange-cli"

// ErrNotFound is returned when no credential is stored for a host.
var ErrNotFound = errors.New("no credentials found in keyring")

// Set stores the token for host.
func Set(host, token string) error {
	if err := gokeyring.Set(service, host, token); err != nil {
		return fmt.Errorf("storing credentials in keyring: %w", err)
	}
	return nil
}

// Get returns the token stored for host, or ErrNotFound.
func Get(host string) (string, error) {
	token, err := gokeyring.Get(service, host)
	if err != nil {
		if errors.Is(err, gokeyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("reading credentials from keyring: %w", err)
	}
	return token, nil
}

// Delete removes the token stored for host, or returns ErrNotFound.
func Delete(host string) error {
	if err := gokeyring.Delete(service, host); err != nil {
		if errors.Is(err, gokeyring.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("deleting credentials from keyring: %w", err)
	}
	return nil
}

// Lookup adapts Get to the config token-resolution lookup signature.
func Lookup(host string) (string, bool, error) {
	token, err := Get(host)
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

// HostKey normalizes a host or URL into the keyring key: scheme and path are
// stripped, the port is kept ("https://api.zetic.ai:8443/v1" -> "api.zetic.ai:8443").
func HostKey(host string) string {
	h := strings.TrimSpace(host)
	if strings.Contains(h, "://") {
		if u, err := url.Parse(h); err == nil && u.Host != "" {
			return u.Host
		}
	}
	if i := strings.Index(h, "/"); i >= 0 {
		h = h[:i]
	}
	return h
}

// SetOAuth stores OAuth credentials for host on a separate key.
func SetOAuth(host string, creds config.OAuthCredentials) error {
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshaling oauth credentials: %w", err)
	}
	if err := gokeyring.Set(service, host+".oauth", string(data)); err != nil {
		return fmt.Errorf("storing oauth credentials in keyring: %w", err)
	}
	return nil
}

// GetOAuth returns OAuth credentials for host or ErrNotFound.
func GetOAuth(host string) (*config.OAuthCredentials, error) {
	raw, err := gokeyring.Get(service, host+".oauth")
	if err != nil {
		if errors.Is(err, gokeyring.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("reading oauth credentials from keyring: %w", err)
	}
	var creds config.OAuthCredentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return nil, fmt.Errorf("parsing oauth credentials: %w", err)
	}
	return &creds, nil
}

// DeleteOAuth removes OAuth credentials for host.
func DeleteOAuth(host string) error {
	if err := gokeyring.Delete(service, host+".oauth"); err != nil {
		if errors.Is(err, gokeyring.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("deleting oauth credentials from keyring: %w", err)
	}
	return nil
}

// LookupOAuth adapts GetOAuth to the ResolveOAuth lookup signature.
func LookupOAuth(host string) (*config.OAuthCredentials, bool, error) {
	creds, err := GetOAuth(host)
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return creds, true, nil
}
