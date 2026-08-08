package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/zetic-ai/melange-cli/internal/browser"
	"github.com/zetic-ai/melange-cli/internal/config"
)

// Sentinel errors for typed handling in callers.
var (
	ErrLoopbackListen = errors.New("loopback listen")
	ErrOAuthTimeout   = errors.New("oauth timeout")
)

func LoginFlow(ctx context.Context, issuerHost string, out io.Writer) (*config.OAuthCredentials, error) {
	transportMu.RLock()
	tr := Transport
	transportMu.RUnlock()
	return LoginFlowWithTransport(ctx, issuerHost, out, tr)
}

// LoginFlowWithTransport is the transport-injected variant.
func LoginFlowWithTransport(ctx context.Context, issuerHost string, out io.Writer, transport http.RoundTripper) (*config.OAuthCredentials, error) {
	return LoginFlowWithOptionsWithTransport(ctx, issuerHost, out, false, transport)
}

func LoginFlowWithOptions(ctx context.Context, issuerHost string, out io.Writer, noBrowser bool) (*config.OAuthCredentials, error) {
	transportMu.RLock()
	tr := Transport
	transportMu.RUnlock()
	return LoginFlowWithOptionsWithTransport(ctx, issuerHost, out, noBrowser, tr)
}

func LoginFlowWithOptionsWithTransport(ctx context.Context, issuerHost string, out io.Writer, noBrowser bool, transport http.RoundTripper) (*config.OAuthCredentials, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLoopbackListen, err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	var disc *Discovery
	disc, discErr := DiscoverWithTransport(ctx, issuerHost, transport)
	if discErr != nil {
		disc = fallbackDiscovery(issuerHost)
	}
	if disc.RevocationEndpoint == "" {
		disc.RevocationEndpoint = fallbackDiscovery(issuerHost).RevocationEndpoint
	}

	clientID, err := RegisterClientWithTransport(ctx, disc.RegistrationEndpoint, redirectURI, transport)
	if err != nil {
		return nil, err
	}

	creds, err := doLoginAttemptWithTransport(ctx, disc, issuerHost, clientID, redirectURI, ln, out, true, noBrowser, transport)
	if err != nil {
		if oe, ok := err.(*OAuthError); ok && oe.Code == "invalid_target" {
			if os.Getenv("MELANGE_DEBUG") != "" {
				fmt.Fprintln(os.Stderr, "resource not allowlisted, retrying authorize without resource")
			}
			if out != nil {
				fmt.Fprintln(out, "! Resource https://api.zetic.ai not allowlisted — continuing without resource binding")
			}
			// First attempt's Serve was closed but the listener may still hold the
			// port. Close it and retry on a fresh ephemeral port with a new DCR
			// (exact redirect_uris match). This avoids the Close+Listen same-port
			// race that flakes under -race (connection refused) on ubuntu/macOS.
			_ = ln.Close()
			ln2, err2 := net.Listen("tcp", "127.0.0.1:0")
			if err2 != nil {
				return nil, fmt.Errorf("%w: %w", ErrLoopbackListen, err2)
			}
			defer ln2.Close()
			port2 := ln2.Addr().(*net.TCPAddr).Port
			redirectURI2 := fmt.Sprintf("http://127.0.0.1:%d/callback", port2)
			clientID2, err2 := RegisterClientWithTransport(ctx, disc.RegistrationEndpoint, redirectURI2, transport)
			if err2 != nil {
				return nil, err2
			}
			return doLoginAttemptWithTransport(ctx, disc, issuerHost, clientID2, redirectURI2, ln2, out, false, noBrowser, transport)
		}
		return nil, err
	}
	return creds, nil
}

//nolint:unused // kept for backward compat; use doLoginAttemptWithTransport
func doLoginAttempt(ctx context.Context, disc *Discovery, issuerHost, clientID, redirectURI string, ln net.Listener, out io.Writer, withResource bool, noBrowser bool) (*config.OAuthCredentials, error) {
	transportMu.RLock()
	tr := Transport
	transportMu.RUnlock()
	return doLoginAttemptWithTransport(ctx, disc, issuerHost, clientID, redirectURI, ln, out, withResource, noBrowser, tr)
}

func doLoginAttemptWithTransport(ctx context.Context, disc *Discovery, issuerHost, clientID, redirectURI string, ln net.Listener, out io.Writer, withResource bool, noBrowser bool, transport http.RoundTripper) (*config.OAuthCredentials, error) {
	verifier, err := generateVerifier()
	if err != nil {
		ln.Close()
		return nil, err
	}
	challenge := challengeFromVerifier(verifier)
	state, err := generateState()
	if err != nil {
		ln.Close()
		return nil, err
	}

	authURL := buildAuthorizeURL(disc.AuthorizationEndpoint, clientID, redirectURI, challenge, state, withResource)

	resultCh := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", loopbackHandler(state, resultCh))
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	if out != nil {
		fmt.Fprintf(out, "Opening %s\n", authURL)
		fmt.Fprintf(out, "If browser doesn't open, visit: %s\n", authURL)
	}
	if !noBrowser {
		if err := browser.Open(authURL); err != nil {
			if out != nil {
				fmt.Fprintf(out, "Browser open failed: %v\n", err)
			}
			if os.Getenv("MELANGE_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "browser open failed: %v\n", err)
			}
		}
	}

	ctx90, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	select {
	case <-ctx90.Done():
		return nil, fmt.Errorf("%w waiting for callback (port %d) — if using SSH, forward: ssh -L %d:127.0.0.1:%d user@host", ErrOAuthTimeout, portFromListener(ln), portFromListener(ln), portFromListener(ln))
	case res := <-resultCh:
		if res.Err != nil {
			return nil, res.Err
		}
		resource := ""
		if withResource {
			resource = DefaultResource
		}
		tok, err := ExchangeCodeWithTransport(ctx, issuerHost, clientID, res.Code, verifier, redirectURI, resource, transport)
		if err != nil {
			if oauthErr, ok := err.(*OAuthError); ok && oauthErr.Code == "invalid_target" && withResource {
				if out != nil {
					fmt.Fprintln(out, "! Resource https://api.zetic.ai not allowlisted — continuing without resource binding")
				}
				tok2, err2 := ExchangeCodeWithTransport(ctx, issuerHost, clientID, res.Code, verifier, redirectURI, "", transport)
				if err2 != nil {
					return nil, err2
				}
				tok = tok2
			} else {
				return nil, err
			}
		}
		expiry := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Add(-30 * time.Second)
		return &config.OAuthCredentials{
			AccessToken:  tok.AccessToken,
			RefreshToken: tok.RefreshToken,
			Expiry:       expiry,
			ClientID:     clientID,
			Scope:        tok.Scope,
			TokenType:    tok.TokenType,
		}, nil
	}
}

func buildAuthorizeURL(authEndpoint, clientID, redirectURI, challenge, state string, withResource bool) string {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", clientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("scope", DefaultScope)
	v.Set("code_challenge", challenge)
	v.Set("code_challenge_method", "S256")
	v.Set("state", state)
	if withResource {
		v.Set("resource", DefaultResource)
	}
	sep := "?"
	if strings.Contains(authEndpoint, "?") {
		sep = "&"
	}
	return authEndpoint + sep + v.Encode()
}

func portFromListener(ln net.Listener) int {
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}
