package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// newTestRetry returns a retry transport whose sleeps are recorded instead of
// executed.
func newTestRetry(base http.RoundTripper) (*retryTransport, *[]time.Duration) {
	sleeps := &[]time.Duration{}
	rt := newRetryTransport(base)
	rt.sleep = func(_ context.Context, d time.Duration) error {
		*sleeps = append(*sleeps, d)
		return nil
	}
	return rt, sleeps
}

func TestRetry502ThenSuccessGET(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(502, "bad gateway"))
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "ok"))

	rt, sleeps := newTestRetry(reg)
	req, _ := http.NewRequest("GET", "https://api.zetic.ai/v1/me", nil)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 200, resp.StatusCode)
	assert.Len(t, reg.Requests, 2)
	require.Len(t, *sleeps, 1)
	// Jittered exponential backoff, base 500ms: first delay in [250ms, 500ms).
	assert.GreaterOrEqual(t, (*sleeps)[0], 250*time.Millisecond)
	assert.Less(t, (*sleeps)[0], 500*time.Millisecond)
}

func TestRetry429HonorsRetryAfter(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(
		httpmock.REST("GET", "/v1/me"),
		httpmock.WithHeader(httpmock.StatusStringResponse(429, "slow down"), "Retry-After", "3"),
	)
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "ok"))

	rt, sleeps := newTestRetry(reg)
	req, _ := http.NewRequest("GET", "https://api.zetic.ai/v1/me", nil)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 200, resp.StatusCode)
	require.Len(t, *sleeps, 1)
	assert.Equal(t, 3*time.Second, (*sleeps)[0], "Retry-After must be honored exactly")
}

func TestRetryRetryAfterCappedAt30s(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(
		httpmock.REST("GET", "/v1/me"),
		httpmock.WithHeader(httpmock.StatusStringResponse(429, ""), "Retry-After", "120"),
	)
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "ok"))

	rt, sleeps := newTestRetry(reg)
	req, _ := http.NewRequest("GET", "https://api.zetic.ai/v1/me", nil)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, *sleeps, 1)
	assert.Equal(t, 30*time.Second, (*sleeps)[0])
}

func TestRetryAfterLongerThanRemainingRequestBudgetSurfaces429(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(
		httpmock.REST("GET", "/v1/me"),
		httpmock.WithHeader(httpmock.StatusStringResponse(429, "slow down"), "Retry-After", "3"),
	)

	rt, sleeps := newTestRetry(reg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.zetic.ai/v1/me", nil)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Len(t, reg.Requests, 1, "an impossible retry must not hide the original 429 behind a deadline")
	assert.Empty(t, *sleeps, "the retry transport must not sleep past the request budget")
}

func TestRetryPOSTNotRetried(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/models"), httpmock.StatusStringResponse(502, "bad gateway"))

	rt, sleeps := newTestRetry(reg)
	req, _ := http.NewRequest("POST", "https://api.zetic.ai/v1/models", strings.NewReader("{}"))
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 502, resp.StatusCode)
	assert.Len(t, reg.Requests, 1)
	assert.Empty(t, *sleeps)
}

func TestRetryPOSTWithIdempotencyKeyRetried(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/models"), httpmock.StatusStringResponse(503, "unavailable"))
	reg.Register(httpmock.REST("POST", "/v1/models"), httpmock.StatusStringResponse(200, "ok"))

	rt, _ := newTestRetry(reg)
	req, _ := http.NewRequest("POST", "https://api.zetic.ai/v1/models", strings.NewReader(`{"n":1}`))
	req.Header.Set("Idempotency-Key", "idem-1")
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 200, resp.StatusCode)
	require.Len(t, reg.Requests, 2)

	// The replayed request must carry the body again (via GetBody).
	replayed, err := io.ReadAll(reg.Requests[1].Body)
	require.NoError(t, err)
	assert.Equal(t, `{"n":1}`, string(replayed))
}

func TestRetryPUTWithReplayableBody(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("PUT", "/v1/default"), httpmock.StatusStringResponse(503, "unavailable"))
	reg.Register(httpmock.REST("PUT", "/v1/default"), httpmock.StatusStringResponse(200, "ok"))

	rt, _ := newTestRetry(reg)
	req, _ := http.NewRequest("PUT", "https://api.zetic.ai/v1/default", strings.NewReader(`{"enabled":true}`))
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 200, resp.StatusCode)
	assert.Len(t, reg.Requests, 2, "PUT is idempotent and safe to replay when its body is rewindable")
}

func TestRetryPATCHRequiresExplicitReplaySafeContext(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("PATCH", "/v1/repos/acme/model"), httpmock.StatusStringResponse(503, "unavailable"))
	reg.Register(httpmock.REST("PATCH", "/v1/repos/acme/model"), httpmock.StatusStringResponse(200, "ok"))

	rt, _ := newTestRetry(reg)
	ctx := WithReplaySafe(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "PATCH", "https://api.zetic.ai/v1/repos/acme/model", strings.NewReader(`{"description":"x"}`))
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 200, resp.StatusCode)
	assert.Len(t, reg.Requests, 2)
}

func TestRetryPATCHNotRetriedWithoutReplaySafeMarker(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("PATCH", "/v1/repos/acme/model"), httpmock.StatusStringResponse(503, "unavailable"))

	rt, sleeps := newTestRetry(reg)
	req, _ := http.NewRequest("PATCH", "https://api.zetic.ai/v1/repos/acme/model", strings.NewReader(`{"description":"x"}`))
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 503, resp.StatusCode)
	assert.Len(t, reg.Requests, 1)
	assert.Empty(t, *sleeps)
}

func TestRetryNoRetryOn429SurfacesImmediately(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/dl"),
		httpmock.WithHeader(httpmock.StatusStringResponse(429, "quota exceeded"), "Retry-After", "3"))

	rt, sleeps := newTestRetry(reg)
	req, _ := http.NewRequestWithContext(WithNoRetryOn429(context.Background()),
		"POST", "https://api.zetic.ai/v1/dl", nil)
	req.Header.Set("Idempotency-Key", "idem-1")
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 429, resp.StatusCode)
	assert.Len(t, reg.Requests, 1,
		"a 429 on an exempted request is not transient (e.g. quota) and must not burn retry attempts")
	assert.Empty(t, *sleeps)
}

func TestRetryNoRetryOn429StillRetries5xx(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/dl"), httpmock.StatusStringResponse(502, "bad gateway"))
	reg.Register(httpmock.REST("POST", "/v1/dl"), httpmock.StatusStringResponse(200, "ok"))

	rt, _ := newTestRetry(reg)
	req, _ := http.NewRequestWithContext(WithNoRetryOn429(context.Background()),
		"POST", "https://api.zetic.ai/v1/dl", nil)
	req.Header.Set("Idempotency-Key", "idem-1")
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 200, resp.StatusCode)
	assert.Len(t, reg.Requests, 2, "the exemption is 429-specific; transient 5xx retries stay")
}

func TestRetryBodyWithoutGetBodyNotRetried(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/models"), httpmock.StatusStringResponse(502, ""))

	rt, sleeps := newTestRetry(reg)
	req, _ := http.NewRequest("POST", "https://api.zetic.ai/v1/models", strings.NewReader("{}"))
	req.Header.Set("Idempotency-Key", "idem-1")
	req.GetBody = nil // simulate a non-replayable body
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 502, resp.StatusCode)
	assert.Len(t, reg.Requests, 1)
	assert.Empty(t, *sleeps)
}

func TestRetryMaxFourAttempts(t *testing.T) {
	reg := &httpmock.Registry{}
	for range 5 {
		reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(503, "down"))
	}

	rt, sleeps := newTestRetry(reg)
	req, _ := http.NewRequest("GET", "https://api.zetic.ai/v1/me", nil)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 503, resp.StatusCode)
	assert.Len(t, reg.Requests, 4, "max 4 attempts total")
	assert.Len(t, *sleeps, 3)
}

func TestRetryConnectionError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.ErrorResponse(errors.New("connection refused")))
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "ok"))

	rt, _ := newTestRetry(reg)
	req, _ := http.NewRequest("GET", "https://api.zetic.ai/v1/me", nil)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 200, resp.StatusCode)
}

func TestRetryConnectionErrorExhausted(t *testing.T) {
	reg := &httpmock.Registry{}
	boom := errors.New("connection refused")
	for range 4 {
		reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.ErrorResponse(boom))
	}

	rt, _ := newTestRetry(reg)
	req, _ := http.NewRequest("GET", "https://api.zetic.ai/v1/me", nil)
	resp, err := rt.RoundTrip(req) //nolint:bodyclose
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, boom)
	assert.Len(t, reg.Requests, 4)
}

func TestRetryBackoffCancellationReturnsPromptly(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(
		httpmock.REST("GET", "/v1/me"),
		httpmock.WithHeader(httpmock.StatusStringResponse(429, ""), "Retry-After", "30"),
	)

	// Production sleep: context-aware, so cancellation interrupts the backoff.
	rt := newRetryTransport(reg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.zetic.ai/v1/me", nil)

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	resp, err := rt.RoundTrip(req) //nolint:bodyclose
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, resp)
	assert.Less(t, elapsed, 5*time.Second, "cancellation must not wait out the 30s Retry-After")
}

func TestRetryCertificateErrorNotRetried(t *testing.T) {
	reg := &httpmock.Registry{}
	certErr := &url.Error{
		Op:  "Get",
		URL: "https://api.zetic.ai/v1/me",
		Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
	}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.ErrorResponse(certErr))

	rt, sleeps := newTestRetry(reg)
	req, _ := http.NewRequest("GET", "https://api.zetic.ai/v1/me", nil)
	resp, err := rt.RoundTrip(req) //nolint:bodyclose
	require.Error(t, err)

	var unknownCA x509.UnknownAuthorityError
	assert.ErrorAs(t, err, &unknownCA)
	assert.Nil(t, resp)
	assert.Len(t, reg.Requests, 1, "certificate errors must not be retried")
	assert.Empty(t, *sleeps)
}

func TestRetryECONNRESETRetried(t *testing.T) {
	reg := &httpmock.Registry{}
	reset := &url.Error{Op: "Get", URL: "https://api.zetic.ai/v1/me", Err: syscall.ECONNRESET}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.ErrorResponse(reset))
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "ok"))

	rt, _ := newTestRetry(reg)
	req, _ := http.NewRequest("GET", "https://api.zetic.ai/v1/me", nil)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 200, resp.StatusCode)
	assert.Len(t, reg.Requests, 2)
}

func TestRetryDoesNotMutateCallerRequest(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/models"), httpmock.StatusStringResponse(503, "unavailable"))
	reg.Register(httpmock.REST("POST", "/v1/models"), httpmock.StatusStringResponse(200, "ok"))

	rt, _ := newTestRetry(reg)
	req, _ := http.NewRequest("POST", "https://api.zetic.ai/v1/models", strings.NewReader(`{"n":1}`))
	req.Header.Set("Idempotency-Key", "idem-1")
	origBody := req.Body

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 200, resp.StatusCode)
	require.Len(t, reg.Requests, 2)
	assert.True(t, req.Body == origBody, "retry must not swap the caller's request body")
}

// ---------------------------------------------------------------------------
// auth transport host matching
// ---------------------------------------------------------------------------

// authRequest sends one GET through an authTransport bound to cfgHost and
// returns the Authorization header the base transport saw.
func authRequest(t *testing.T, cfgHost, rawURL string) string {
	t.Helper()
	reg := &httpmock.Registry{}
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	reg.Register(httpmock.REST("GET", strings.TrimPrefix(u.Path, "/")),
		httpmock.StatusStringResponse(200, "ok"))

	at := &authTransport{base: reg, host: cfgHost, token: "ztp_secret"}
	req, err := http.NewRequest("GET", rawURL, nil)
	require.NoError(t, err)
	resp, err := at.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, reg.Requests, 1)
	return reg.Requests[0].Header.Get("Authorization")
}

func TestAuthHostASCIICaseInsensitive(t *testing.T) {
	assert.Equal(t, "Bearer ztp_secret",
		authRequest(t, "API.Zetic.AI", "https://api.zetic.ai/v1/me"),
		"host comparison stays case-insensitive for ASCII")
}

func TestAuthHostUnicodeFoldingRejected(t *testing.T) {
	// U+212A (KELVIN SIGN) simple-folds to 'k'; a homoglyph host must never
	// receive the token even though strings.EqualFold would match it.
	assert.Empty(t,
		authRequest(t, "kelvin.example.com", "https://\u212Aelvin.example.com/v1/me"),
		"Unicode-folded homoglyph host must not match the configured host")
}

func TestAuthHostDefaultPortNormalization(t *testing.T) {
	tests := []struct {
		name    string
		cfgHost string
		rawURL  string
		want    bool
	}{
		{"https explicit 443 vs bare", "api.zetic.ai", "https://api.zetic.ai:443/v1/me", true},
		{"https bare vs configured 443", "api.zetic.ai:443", "https://api.zetic.ai/v1/me", true},
		{"https non-default port", "api.zetic.ai", "https://api.zetic.ai:8443/v1/me", false},
		{"http explicit 80 vs bare", "localhost", "http://localhost:80/v1/me", true},
		{"http bare vs configured 80", "localhost:80", "http://localhost/v1/me", true},
		{"http non-default port", "localhost", "http://localhost:8080/v1/me", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := authRequest(t, tt.cfgHost, tt.rawURL)
			if tt.want {
				assert.Equal(t, "Bearer ztp_secret", got)
			} else {
				assert.Empty(t, got, "token must stay host-bound across ports")
			}
		})
	}
}

func TestRetryBackoffDoubles(t *testing.T) {
	reg := &httpmock.Registry{}
	for range 4 {
		reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(502, ""))
	}

	rt, sleeps := newTestRetry(reg)
	req, _ := http.NewRequest("GET", "https://api.zetic.ai/v1/me", nil)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, *sleeps, 3)
	// Jittered delays land in [d/2, d) for d = 500ms, 1s, 2s.
	for i, base := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second} {
		assert.GreaterOrEqual(t, (*sleeps)[i], base/2, "sleep %d", i)
		assert.Less(t, (*sleeps)[i], base, "sleep %d", i)
	}
}
