package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// auth transport
// ---------------------------------------------------------------------------

// errPlaintextHTTP guards against leaking credentials to non-TLS endpoints.
var errPlaintextHTTP = errors.New("refusing to send credentials over plaintext HTTP")

// authTransport sets the Authorization and User-Agent headers, and refuses to
// attach credentials to plaintext HTTP requests targeting non-loopback hosts.
//
// The token is host-bound: it is only attached when the request targets the
// configured host. This matters because http.Client re-issues redirected
// requests through the whole transport chain, so without this check a
// cross-host redirect (e.g. to a storage bucket) would re-add the token that
// net/http itself deliberately strips.
type authTransport struct {
	base      http.RoundTripper
	host      string // configured API host (URL.Host, including any port)
	token     string
	userAgent string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if t.userAgent != "" {
		req.Header.Set("User-Agent", t.userAgent)
	}
	if t.token != "" && strings.EqualFold(req.URL.Host, t.host) {
		if req.URL.Scheme == "http" && !isLoopback(req.URL.Hostname()) {
			return nil, errPlaintextHTTP
		}
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}

// isLoopback reports whether hostname is a local loopback address.
func isLoopback(hostname string) bool {
	switch hostname {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// retry transport
// ---------------------------------------------------------------------------

const (
	retryMaxAttempts = 4
	retryBaseDelay   = 500 * time.Millisecond
	retryAfterCap    = 30 * time.Second
)

// retryTransport retries idempotent requests (GET/HEAD, or any request with
// an Idempotency-Key header) on 429/502/503/504 and connection errors, with
// jittered exponential backoff. Retry-After is honored (capped) on 429s.
type retryTransport struct {
	base  http.RoundTripper
	sleep func(context.Context, time.Duration) error // injectable for tests
}

func newRetryTransport(base http.RoundTripper) *retryTransport {
	return &retryTransport{base: base, sleep: sleepContext}
}

// sleepContext blocks for d or until ctx is done, returning ctx.Err() when
// interrupted so a canceled request never waits out the backoff delay.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !retryEligible(req) {
		return t.base.RoundTrip(req)
	}
	// Per the RoundTripper contract, never mutate the caller's request.
	req = req.Clone(req.Context())

	var resp *http.Response
	var err error
	for attempt := 0; ; attempt++ {
		if attempt > 0 && req.Body != nil {
			body, bodyErr := req.GetBody()
			if bodyErr != nil {
				return nil, fmt.Errorf("rewinding request body for retry: %w", bodyErr)
			}
			req.Body = body
		}

		resp, err = t.base.RoundTrip(req)

		var delay time.Duration
		switch {
		case err != nil:
			if !isRetryableTransportErr(err) {
				return nil, err
			}
			delay = backoff(attempt)
		case retryableStatus(resp.StatusCode):
			if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
				delay = min(ra, retryAfterCap)
			} else {
				delay = backoff(attempt)
			}
		default:
			return resp, nil
		}

		if attempt == retryMaxAttempts-1 || req.Context().Err() != nil {
			return resp, err
		}
		if resp != nil {
			// Drain so the connection can be reused, then discard.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
			_ = resp.Body.Close()
		}
		if sleepErr := t.sleep(req.Context(), delay); sleepErr != nil {
			return nil, sleepErr
		}
	}
}

// isRetryableTransportErr reports whether a transport-level error is worth
// retrying: timeouts and connection resets/refusals are transient, while
// context cancellation and TLS certificate failures are not.
func isRetryableTransportErr(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var (
		certVerify  *tls.CertificateVerificationError
		unknownCA   x509.UnknownAuthorityError
		hostnameErr x509.HostnameError
		certInvalid x509.CertificateInvalidError
	)
	if errors.As(err, &certVerify) || errors.As(err, &unknownCA) ||
		errors.As(err, &hostnameErr) || errors.As(err, &certInvalid) {
		return false
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// url.Error and friends often carry only stringly-typed causes.
	msg := err.Error()
	return strings.Contains(msg, "connection reset") || strings.Contains(msg, "connection refused")
}

// retryEligible reports whether the request may be retried at all: it must be
// idempotent (GET/HEAD or carrying an Idempotency-Key) and its body, if any,
// must be replayable via GetBody.
func retryEligible(req *http.Request) bool {
	idempotent := req.Method == http.MethodGet ||
		req.Method == http.MethodHead ||
		req.Header.Get("Idempotency-Key") != ""
	if !idempotent {
		return false
	}
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return false
	}
	return true
}

// retryableStatus reports whether the status code triggers a retry.
func retryableStatus(status int) bool {
	return (&Error{StatusCode: status}).Retryable()
}

// backoff returns a jittered exponential delay: base 500ms doubled per
// attempt, jittered into [d/2, d).
func backoff(attempt int) time.Duration {
	d := retryBaseDelay << attempt
	return d/2 + rand.N(d/2)
}

// ---------------------------------------------------------------------------
// debug transport
// ---------------------------------------------------------------------------

// debugTransport logs "> METHOD url" and "< status durationms" lines. It never
// writes headers, so the Authorization value cannot leak into debug output.
type debugTransport struct {
	base http.RoundTripper
	out  io.Writer
}

func (t *debugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	fmt.Fprintf(t.out, "> %s %s\n", req.Method, req.URL)
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		fmt.Fprintf(t.out, "< error %dms\n", elapsed)
		return resp, err
	}
	fmt.Fprintf(t.out, "< %d %dms\n", resp.StatusCode, elapsed)
	return resp, nil
}
