package model

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

func TestArtifactRetryAfterHonorsSecondsAndHTTPDate(t *testing.T) {
	originalNow := artifactRetryNow
	t.Cleanup(func() { artifactRetryNow = originalNow })
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	artifactRetryNow = func() time.Time { return now }

	seconds, ok := parseArtifactRetryAfter("7")
	assert.True(t, ok)
	assert.Equal(t, 7*time.Second, seconds)
	bounded, ok := parseArtifactRetryAfter("600")
	assert.True(t, ok)
	assert.Equal(t, 30*time.Second, bounded, "untrusted Retry-After must not create an unbounded sleep")

	httpDate := now.Add(11 * time.Second).Format(http.TimeFormat)
	dateDelay, ok := parseArtifactRetryAfter(httpDate)
	assert.True(t, ok)
	assert.Equal(t, 11*time.Second, dateDelay)

	pastDelay, ok := parseArtifactRetryAfter(now.Add(-time.Minute).Format(http.TimeFormat))
	assert.True(t, ok)
	assert.Zero(t, pastDelay)
}

func TestArtifactInactivityTimeoutResetsOnProgress(t *testing.T) {
	originalTimeout := artifactTransferInactivityTimeout
	t.Cleanup(func() { artifactTransferInactivityTimeout = originalTimeout })
	artifactTransferInactivityTimeout = 100 * time.Millisecond
	payload := []byte("four")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		flusher := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		for _, b := range payload {
			time.Sleep(30 * time.Millisecond)
			_, _ = w.Write([]byte{b})
			flusher.Flush()
		}
	}))
	defer srv.Close()

	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	size := len(payload)
	art := gen.DownloadArtifact{Name: "model.bin", Url: srv.URL + "/artifact?signature=PROGRESSSECRET", Size: &size}
	dest := filepath.Join(t.TempDir(), "model.bin")

	written, err := downloadArtifact(context.Background(), &downloadOptions{f: f}, art, dest)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), written)
}

func TestArtifactInactivityTimeoutIsRetriedAndRedacted(t *testing.T) {
	originalTimeout := artifactTransferInactivityTimeout
	t.Cleanup(func() { artifactTransferInactivityTimeout = originalTimeout })
	artifactTransferInactivityTimeout = 25 * time.Millisecond
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "x")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	size := 4
	art := gen.DownloadArtifact{Name: "model.bin", Url: srv.URL + "/artifact?signature=INACTIVITYSECRET", Size: &size}
	_, err := downloadArtifact(context.Background(), &downloadOptions{f: f}, art, filepath.Join(t.TempDir(), "model.bin"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inactive")
	assert.NotContains(t, err.Error(), "INACTIVITYSECRET")
	assert.Equal(t, int32(artifactMaxAttempts), attempts.Load())
}

func TestArtifactRetryDelayIsBoundedAndUsesRetryAfterExactly(t *testing.T) {
	originalJitter := artifactRetryJitter
	t.Cleanup(func() { artifactRetryJitter = originalJitter })
	artifactRetryJitter = func(max time.Duration) time.Duration { return max }

	for attempt := 0; attempt < artifactMaxAttempts; attempt++ {
		delay, retry := artifactRetryDelay(context.Background(), &artifactStatusError{
			name:   "model.bin",
			status: http.StatusBadGateway,
		}, attempt)
		assert.True(t, retry)
		assert.LessOrEqual(t, delay, 3*time.Second, fmt.Sprintf("attempt %d", attempt))
	}

	delay, retry := artifactRetryDelay(context.Background(), &artifactStatusError{
		name:          "model.bin",
		status:        http.StatusTooManyRequests,
		retryAfter:    9 * time.Second,
		hasRetryAfter: true,
	}, 0)
	assert.True(t, retry)
	assert.Equal(t, 9*time.Second, delay, "Retry-After must not be shortened or jittered")
}
