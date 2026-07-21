// Package keyring stores Melange credentials in the OS keychain via
// zalando/go-keyring. Entries live under the "melange-cli" service, keyed by
// host (scheme stripped, port kept — see HostKey).
package keyring

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	gokeyring "github.com/zalando/go-keyring"
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
func Lookup(host string) (string, bool) {
	token, err := Get(host)
	if err != nil {
		return "", false
	}
	return token, true
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
