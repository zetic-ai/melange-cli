package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
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
	rt.sleep = func(d time.Duration) { *sleeps = append(*sleeps, d) }
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
