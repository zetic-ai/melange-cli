package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// GCS resumable protocol constants.
const (
	chunkGranularity       = 256 * 1024 // GCS requires 256 KiB multiples
	defaultChunkSize       = 8 * 1024 * 1024
	defaultMaxRetries      = 4
	statusResumeIncomplete = 308
	retryBase              = 500 * time.Millisecond
	maxDrainBytes          = 32 * 1024
)

// ErrSessionExpired reports a non-retryable 4xx from GCS: the signed URL or
// resumable session is no longer valid. The caller should reissue the file's
// upload URL through the API and restart that file's session.
var ErrSessionExpired = errors.New("upload URL or resumable session expired")

// Uploader speaks the GCS resumable upload protocol against v4 signed
// resumable-start URLs.
//
// It deliberately runs over a BARE http.Client: no Authorization header, no
// melange transport chain (auth/retry/debug) — GCS must never see the PAT,
// signed URLs carry their own credentials, and debug logging must never see
// signed URLs or session URIs. Upload-specific retry lives here instead: on
// retryable failures the committed offset is re-queried from the server and
// the transfer continues from there, never resending acknowledged bytes.
type Uploader struct {
	Client       *http.Client
	ChunkSize    int64         // rounded up to a 256 KiB multiple; default 8 MiB
	StallTimeout time.Duration // per-chunk inactivity budget; 0 = no limit
	MaxRetries   int           // consecutive retryable failures tolerated per call (default 4)
	// Sleep blocks between retries; nil = timer sleep. Injectable for tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (u *Uploader) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return http.DefaultClient
}

func (u *Uploader) chunkSize() int64 {
	c := u.ChunkSize
	if c <= 0 {
		c = defaultChunkSize
	}
	if rem := c % chunkGranularity; rem != 0 {
		c += chunkGranularity - rem
	}
	return c
}

func (u *Uploader) maxRetries() int {
	if u.MaxRetries > 0 {
		return u.MaxRetries
	}
	if u.MaxRetries == 0 {
		return defaultMaxRetries
	}
	return 0 // negative = no retries (tests)
}

func (u *Uploader) sleep(ctx context.Context, d time.Duration) error {
	if u.Sleep != nil {
		return u.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (u *Uploader) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if u.StallTimeout > 0 {
		return context.WithTimeout(ctx, u.StallTimeout)
	}
	return ctx, func() {}
}

// StartSession opens a resumable session: POST to the signed URL with
// `x-goog-resumable: start` and an empty body; the Location response header
// is the resumable session URI (a bearer credential — never log it).
func (u *Uploader) StartSession(ctx context.Context, uploadURL string) (string, error) {
	for failures := 0; ; {
		rctx, cancel := u.requestContext(ctx)
		req, err := http.NewRequestWithContext(rctx, http.MethodPost, uploadURL, http.NoBody)
		if err != nil {
			cancel()
			return "", fmt.Errorf("starting upload session: %w", redactErr(err))
		}
		req.Header.Set("x-goog-resumable", "start")
		req.ContentLength = 0

		resp, err := u.client().Do(req)
		status, retryable, err := u.classify(ctx, resp, err)
		cancel()
		if err != nil {
			return "", fmt.Errorf("starting upload session: %w", err)
		}
		switch {
		case status == http.StatusCreated || status == http.StatusOK:
			loc := resp.Header.Get("Location")
			if loc == "" {
				return "", errors.New("starting upload session: GCS returned no Location header")
			}
			return loc, nil
		case retryable:
			failures++
			if failures > u.maxRetries() {
				if status == 0 {
					return "", fmt.Errorf("starting upload session: transient transport failure after %d attempts", failures)
				}
				return "", fmt.Errorf("starting upload session: GCS returned HTTP %d after %d attempts", status, failures)
			}
			if err := u.sleep(ctx, backoffDelay(failures)); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("starting upload session: %w (HTTP %d)", ErrSessionExpired, status)
		}
	}
}

// QueryOffset asks GCS how many bytes of the session are committed: an empty
// PUT with `Content-Range: bytes */<total>`. done reports an already-complete
// upload (HTTP 200/201); otherwise offset is the next byte to send.
func (u *Uploader) QueryOffset(ctx context.Context, sessionURI string, total int64) (offset int64, done bool, err error) {
	for failures := 0; ; {
		rctx, cancel := u.requestContext(ctx)
		req, err := http.NewRequestWithContext(rctx, http.MethodPut, sessionURI, http.NoBody)
		if err != nil {
			cancel()
			return 0, false, fmt.Errorf("querying upload offset: %w", redactErr(err))
		}
		req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", total))
		req.ContentLength = 0

		resp, err := u.client().Do(req)
		status, retryable, err := u.classify(ctx, resp, err)
		cancel()
		if err != nil {
			return 0, false, fmt.Errorf("querying upload offset: %w", err)
		}
		switch {
		case status == http.StatusOK || status == http.StatusCreated:
			return total, true, nil
		case status == statusResumeIncomplete:
			return committedFromRange(resp.Header.Get("Range")), false, nil
		case retryable:
			failures++
			if failures > u.maxRetries() {
				if status == 0 {
					return 0, false, fmt.Errorf("querying upload offset: transient transport failure after %d attempts", failures)
				}
				return 0, false, fmt.Errorf("querying upload offset: GCS returned HTTP %d after %d attempts", status, failures)
			}
			if err := u.sleep(ctx, backoffDelay(failures)); err != nil {
				return 0, false, err
			}
		default:
			return 0, false, fmt.Errorf("querying upload offset: %w (HTTP %d)", ErrSessionExpired, status)
		}
	}
}

// UploadFile PUTs path's bytes to sessionURI in ChunkSize chunks starting at
// offset `from`. On retryable failures (5xx, 408/429, connection errors,
// per-chunk stalls) it re-queries the committed offset and continues from
// there — server-acknowledged bytes are never resent. Non-retryable 4xx
// surfaces as ErrSessionExpired so the caller can reissue the URL.
//
// onCommit, when non-nil, observes every server-committed offset (for
// progress display and state persistence).
func (u *Uploader) UploadFile(ctx context.Context, sessionURI, path string, total, from int64, onCommit func(committed int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only

	commit := func(n int64) {
		if onCommit != nil {
			onCommit(n)
		}
	}

	offset := from
	highWater := from // max committed offset ever observed
	failures := 0
	// recover handles a failed chunk attempt by asking the server what it
	// actually committed. Forward progress past the high-water mark
	// restores full retry credit: a slow but moving link never exhausts
	// MaxRetries, and StallTimeout stays a stall detector — only attempts
	// that commit no new bytes consume the budget. The committed offset is
	// monotonic in the protocol, so a server oscillating it (violation)
	// never re-earns credit and never rewinds us below the high-water mark.
	recover := func(cause error) error {
		committed, done, qerr := u.QueryOffset(ctx, sessionURI, total)
		if qerr != nil {
			return qerr
		}
		if done {
			committed = total
		}
		if committed > highWater {
			failures = 0
			highWater = committed
			offset = committed
			commit(offset)
			return nil
		}
		offset = max(committed, highWater) // never rewind below the high-water mark
		failures++
		if failures > u.maxRetries() {
			return fmt.Errorf("uploading %s: giving up after %d attempts: %w", path, failures, cause)
		}
		return u.sleep(ctx, backoffDelay(failures))
	}

	for offset < total {
		end := min(offset+u.chunkSize(), total) - 1
		status, acked, err := u.putChunk(ctx, sessionURI, f, offset, end, total)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if rerr := recover(err); rerr != nil {
				return rerr
			}
			continue
		}
		switch {
		case status == http.StatusOK || status == http.StatusCreated:
			commit(total)
			return nil
		case status == statusResumeIncomplete:
			// The Range header is authoritative: the server may have
			// committed less than the chunk we sent.
			if acked <= offset {
				if rerr := recover(fmt.Errorf("GCS committed no new bytes (HTTP 308)")); rerr != nil {
					return rerr
				}
				continue
			}
			failures = 0
			offset = acked
			highWater = max(highWater, offset)
			commit(offset)
		case retryableGCSStatus(status):
			if rerr := recover(fmt.Errorf("GCS returned HTTP %d", status)); rerr != nil {
				return rerr
			}
		default:
			return fmt.Errorf("uploading %s: %w (HTTP %d)", path, ErrSessionExpired, status)
		}
	}

	// Reached total without a finalizing 200/201: confirm with the server.
	_, done, err := u.QueryOffset(ctx, sessionURI, total)
	if err != nil {
		return err
	}
	if !done {
		return fmt.Errorf("uploading %s: all bytes sent but GCS did not finalize the object", path)
	}
	commit(total)
	return nil
}

// putChunk sends one Content-Range chunk. It returns the HTTP status and the
// next offset implied by the response's Range header (0 when the header is
// absent or malformed, per committedFromRange). Transport errors come back
// redacted (no URL, no query).
func (u *Uploader) putChunk(ctx context.Context, sessionURI string, f *os.File, offset, end, total int64) (status int, acked int64, err error) {
	cctx := ctx
	if u.StallTimeout > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, u.StallTimeout)
		defer cancel()
	}
	n := end - offset + 1
	req, err := http.NewRequestWithContext(cctx, http.MethodPut, sessionURI, io.NewSectionReader(f, offset, n))
	if err != nil {
		return 0, -1, redactErr(err)
	}
	req.ContentLength = n
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end, total))

	resp, err := u.client().Do(req)
	if err != nil {
		return 0, -1, redactErr(err)
	}
	defer drain(resp)
	return resp.StatusCode, committedFromRange(resp.Header.Get("Range")), nil
}

// classify normalizes a response/error pair: transport errors are redacted
// and non-nil ctx errors win; for responses it reports the status and
// whether it is retryable. The response body is drained unless the caller
// still needs the headers of a 2xx/308 (headers survive draining).
func (u *Uploader) classify(ctx context.Context, resp *http.Response, err error) (status int, retryable bool, _ error) {
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, false, ctxErr
		}
		if retryableGCSTransportError(err) {
			return 0, true, nil
		}
		return 0, false, redactErr(err)
	}
	drain(resp)
	return resp.StatusCode, retryableGCSStatus(resp.StatusCode), nil
}

// retryableGCSTransportError recognizes failures where replaying an empty
// resumable-start/offset-query request is safe and likely useful. Certificate
// and other permanent protocol errors deliberately fall through.
func retryableGCSTransportError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "unexpected eof")
}

// retryableGCSStatus: 5xx, plus 408 (request timeout) and 429 (rate limit).
func retryableGCSStatus(status int) bool {
	return status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
}

// committedFromRange parses a GCS "Range: bytes=0-N" response header into
// the next offset (N+1). Absent or malformed → 0 (nothing committed).
func committedFromRange(h string) int64 {
	var last int64
	if n, _ := fmt.Sscanf(h, "bytes=0-%d", &last); n != 1 {
		return 0
	}
	return last + 1
}

// backoffDelay is a jittered exponential delay: 500ms doubled per failure,
// jittered into [d/2, d).
func backoffDelay(failures int) time.Duration {
	d := retryBase << (failures - 1)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d/2 + rand.N(d/2)
}

// redactErr strips URLs from transport errors. Signed upload URLs and
// resumable session URIs carry credentials in their query strings, and
// url.Error embeds the full URL — only the host may survive.
func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		host := ""
		if parsed, perr := url.Parse(ue.URL); perr == nil {
			host = parsed.Host
		}
		// Do not wrap the underlying cause: custom transports sometimes echo
		// the complete request URL in it, which would reintroduce the signed
		// credential we just removed. Preserve context sentinels so callers can
		// still distinguish cancellation and inactivity timeouts safely.
		if errors.Is(ue.Err, context.Canceled) {
			return fmt.Errorf("%s to %s failed: %w", ue.Op, host, context.Canceled)
		}
		if errors.Is(ue.Err, context.DeadlineExceeded) {
			return fmt.Errorf("%s to %s failed: %w", ue.Op, host, context.DeadlineExceeded)
		}
		return fmt.Errorf("%s to %s failed", ue.Op, host)
	}
	return err
}

// drain consumes and closes a response body so connections are reused.
func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
	_ = resp.Body.Close()
}
