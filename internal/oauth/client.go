package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultScope    = "write"
	DefaultResource = "https://api.zetic.ai"
)

// TokenResponse is the token endpoint response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

// OAuthError represents an OAuth error from callback or token endpoint.
type OAuthError struct {
	Code        string
	Description string
	State       string
}

// TransportError identifies an OAuth HTTP request that failed before an HTTP
// response was received. Callers may offer a non-OAuth fallback for this
// class without masking protocol or authorization-server errors.
type TransportError struct {
	Err error
}

func (e *TransportError) Error() string { return "oauth transport: " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return e.Code
}

// Discovery holds OAuth discovery endpoints.
type Discovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

func httpClientWithTransport(tr http.RoundTripper) *http.Client {
	if tr == nil {
		tr = http.DefaultTransport
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func doRequest(transport http.RoundTripper, req *http.Request) (*http.Response, error) {
	resp, err := httpClientWithTransport(transport).Do(req)
	if err != nil {
		return nil, &TransportError{Err: err}
	}
	return resp, nil
}

func normalizeHost(host string) string {
	h := strings.TrimSuffix(strings.TrimSpace(host), "/")
	if !strings.Contains(h, "://") {
		h = "https://" + h
	}
	return h
}

// DiscoverWithTransport fetches OIDC discovery using the provided transport.
func DiscoverWithTransport(ctx context.Context, issuerHost string, transport http.RoundTripper) (*Discovery, error) {
	issuer := normalizeHost(issuerHost)
	u := issuer + "/.well-known/oauth-authorization-server"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(transport, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var d Discovery
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("discovery missing endpoints")
	}
	// Revocation endpoint may be missing; fallback will fill.
	return &d, nil
}

func fallbackDiscovery(issuerHost string) *Discovery {
	issuer := normalizeHost(issuerHost)
	return &Discovery{
		AuthorizationEndpoint: issuer + "/oauth/authorize",
		TokenEndpoint:         issuer + "/oauth/token",
		RegistrationEndpoint:  issuer + "/oauth/register",
		RevocationEndpoint:    issuer + "/oauth/revoke",
	}
}

// RegisterClientWithTransport performs DCR using the provided registration URL and transport.
func RegisterClientWithTransport(ctx context.Context, registrationURL, redirectURI string, transport http.RoundTripper) (string, error) {
	payload := map[string]any{
		"client_name":                "melange-cli",
		"redirect_uris":              []string{redirectURI},
		"scope":                      "read write",
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	}
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationURL, strings.NewReader(string(data)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := doRequest(transport, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		return "", fmt.Errorf("DCR failed %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.ClientID == "" {
		return "", fmt.Errorf("DCR missing client_id")
	}
	return out.ClientID, nil
}

// ExchangeCodeWithTransport exchanges authorization code for tokens using the provided transport.
func ExchangeCodeWithTransport(ctx context.Context, issuerHost, clientID, code, verifier, redirectURI, resource string, transport http.RoundTripper) (*TokenResponse, error) {
	d, err := DiscoverWithTransport(ctx, issuerHost, transport)
	var tokenURL string
	if err == nil {
		tokenURL = d.TokenEndpoint
	} else {
		tokenURL = fallbackDiscovery(issuerHost).TokenEndpoint
	}
	return exchangeCodeWithURLWithTransport(ctx, tokenURL, clientID, code, verifier, redirectURI, resource, transport)
}

func exchangeCodeWithURLWithTransport(ctx context.Context, tokenURL, clientID, code, verifier, redirectURI, resource string, transport http.RoundTripper) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("client_id", clientID)
	form.Set("redirect_uri", redirectURI)
	if resource != "" {
		form.Set("resource", resource)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doRequest(transport, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		// Check for invalid_target to allow retry.
		var errBody map[string]any
		if json.Unmarshal(body, &errBody) == nil {
			if e, ok := errBody["error"].(string); ok && e == "invalid_target" {
				return nil, &OAuthError{Code: "invalid_target", Description: fmt.Sprint(errBody["error_description"])}
			}
		}
		return nil, fmt.Errorf("token exchange %d: %s", resp.StatusCode, string(body))
	}
	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// RefreshWithTransport exchanges refresh token for new tokens using the provided transport.
func RefreshWithTransport(ctx context.Context, issuerHost, clientID, refreshToken string, transport http.RoundTripper) (*TokenResponse, error) {
	d, err := DiscoverWithTransport(ctx, issuerHost, transport)
	var tokenURL string
	if err == nil {
		tokenURL = d.TokenEndpoint
	} else {
		tokenURL = fallbackDiscovery(issuerHost).TokenEndpoint
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doRequest(transport, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		var errBody map[string]any
		if json.Unmarshal(body, &errBody) == nil {
			if e, ok := errBody["error"].(string); ok {
				return nil, &OAuthError{Code: e, Description: fmt.Sprint(errBody["error_description"])}
			}
		}
		return nil, fmt.Errorf("refresh %d: %s", resp.StatusCode, string(body))
	}
	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// RevokeWithTransport revokes a token using the provided transport.
func RevokeWithTransport(ctx context.Context, issuerHost, clientID, token string, transport http.RoundTripper) error {
	d, err := DiscoverWithTransport(ctx, issuerHost, transport)
	var revokeURL string
	if err == nil && d.RevocationEndpoint != "" {
		revokeURL = d.RevocationEndpoint
	} else {
		revokeURL = fallbackDiscovery(issuerHost).RevocationEndpoint
	}
	form := url.Values{}
	form.Set("token", token)
	if clientID != "" {
		form.Set("client_id", clientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doRequest(transport, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// PKCE helpers

func generateVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func challengeFromVerifier(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
