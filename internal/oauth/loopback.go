package oauth

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// isLoopback reports whether hostname is loopback.
func isLoopback(hostname string) bool {
	if asciiLower(hostname) == "localhost" {
		return true
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, ch := range b {
		if ch >= 'A' && ch <= 'Z' {
			b[i] = ch + ('a' - 'A')
		}
	}
	return string(b)
}

// callbackResult holds code or error from loopback callback.
type callbackResult struct {
	Code  string
	State string
	Err   *OAuthError
}

func loopbackHandler(expectedState string, ch chan callbackResult) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Validate Host is loopback
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		} else if strings.Contains(host, ":") {
			// Fallback split
			if idx := strings.LastIndex(host, ":"); idx >= 0 {
				host = host[:idx]
			}
		}
		if !isLoopback(host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		q := r.URL.Query()
		state := q.Get("state")
		if state != expectedState {
			http.Error(w, "invalid state", http.StatusBadRequest)
			ch <- callbackResult{Err: &OAuthError{Code: "invalid_state", Description: "state mismatch", State: state}}
			return
		}
		if errStr := q.Get("error"); errStr != "" {
			ch <- callbackResult{Err: &OAuthError{Code: errStr, Description: q.Get("error_description"), State: state}}
			fmt.Fprint(w, "You can close this window and return to your terminal.")
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			ch <- callbackResult{Err: &OAuthError{Code: "missing_code", State: state}}
			return
		}
		fmt.Fprint(w, "You can close this window and return to your terminal.")
		ch <- callbackResult{Code: code, State: state}
	}
}
