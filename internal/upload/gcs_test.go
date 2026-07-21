package upload_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/upload"
)

const chunk = 256 * 1024 // minimum GCS chunk granularity, used for tests

// gcsRequest records what the fake GCS server saw.
type gcsRequest struct {
	Method       string
	Path         string
	ContentRange string
	Resumable    string // x-goog-resumable header
	BodyLen      int64
	Auth         string // Authorization header (must always be empty)
	RangeStart   int64  // parsed from Content-Range "bytes a-b/N"; -1 for queries
}

// fakeGCS implements the resumable upload protocol.
type fakeGCS struct {
	t  *testing.T
	mu sync.Mutex

	object    []byte
	committed int64
	complete  bool
	requests  []gcsRequest

	// failPut maps a chunk start offset to a queue of statuses to return
	// (without committing) before accepting that chunk.
	failPut map[int64][]int
	// ackShort maps a chunk start offset to the committed offset the server
	// reports instead of the full chunk (simulating a partial ack).
	ackShort map[int64]int64
	// hangPut delays every data PUT (stall simulation).
	hangPut time.Duration

	srv *httptest.Server
}

func newFakeGCS(t *testing.T) *fakeGCS {
	g := &fakeGCS{t: t, failPut: map[int64][]int{}, ackShort: map[int64]int64{}}
	g.srv = httptest.NewServer(http.HandlerFunc(g.handle))
	t.Cleanup(g.srv.Close)
	return g
}

// signedURL returns the signed resumable-start URL for the fake object.
func (g *fakeGCS) signedURL() string {
	return g.srv.URL + "/upload/obj?X-Goog-Signature=SIGNED_SECRET"
}

func (g *fakeGCS) handle(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	req := gcsRequest{
		Method:       r.Method,
		Path:         r.URL.Path,
		ContentRange: r.Header.Get("Content-Range"),
		Resumable:    r.Header.Get("x-goog-resumable"),
		Auth:         r.Header.Get("Authorization"),
		RangeStart:   -1,
	}

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/upload/obj":
		g.requests = append(g.requests, req)
		w.Header().Set("Location", g.srv.URL+"/session/obj?upload_id=SESSION_SECRET")
		w.WriteHeader(http.StatusCreated)
		return

	case r.Method == http.MethodPut && r.URL.Path == "/session/obj":
		cr := r.Header.Get("Content-Range")
		var total int64
		// committed-offset query: "bytes */N"
		if n, _ := fmt.Sscanf(cr, "bytes */%d", &total); n == 1 {
			g.requests = append(g.requests, req)
			if g.complete {
				w.WriteHeader(http.StatusOK)
				return
			}
			if g.committed > 0 {
				w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", g.committed-1))
			}
			w.WriteHeader(308)
			return
		}
		var from, to int64
		if n, _ := fmt.Sscanf(cr, "bytes %d-%d/%d", &from, &to, &total); n != 3 {
			g.t.Errorf("malformed Content-Range %q", cr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		require.NoError(g.t, err)
		req.BodyLen = int64(len(body))
		req.RangeStart = from
		g.requests = append(g.requests, req)

		if g.hangPut > 0 {
			// A stalled chunk: the server dawdles past the client's stall
			// budget and commits nothing.
			g.mu.Unlock()
			time.Sleep(g.hangPut)
			g.mu.Lock()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if queue := g.failPut[from]; len(queue) > 0 {
			status := queue[0]
			g.failPut[from] = queue[1:]
			w.WriteHeader(status)
			return
		}

		// Integrity contract: a chunk must start exactly at the committed
		// offset — earlier bytes were acked and must never be resent.
		if from != g.committed {
			g.t.Errorf("chunk starts at %d, committed offset is %d (re-upload of acked bytes?)", from, g.committed)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		require.Equal(g.t, to-from+1, int64(len(body)), "body length matches Content-Range")

		g.object = append(g.object[:from], body...)
		newCommitted := to + 1
		if short, ok := g.ackShort[from]; ok {
			delete(g.ackShort, from)
			newCommitted = short
			g.object = g.object[:short]
		}
		g.committed = newCommitted

		if g.committed == total && newCommitted == to+1 {
			g.complete = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", g.committed-1))
		w.WriteHeader(308)
		return
	}
	// Unknown paths behave like GCS with an expired/foreign session: 404.
	w.WriteHeader(http.StatusNotFound)
}

func (g *fakeGCS) dataPuts() []gcsRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []gcsRequest
	for _, r := range g.requests {
		if r.Method == http.MethodPut && r.RangeStart >= 0 {
			out = append(out, r)
		}
	}
	return out
}

func noSleep(context.Context, time.Duration) error { return nil }

func testUploader() *upload.Uploader {
	return &upload.Uploader{
		Client:    &http.Client{},
		ChunkSize: chunk,
		Sleep:     noSleep,
	}
}

func tempFile(t *testing.T, size int) string {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	path := filepath.Join(t.TempDir(), "model.onnx")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func readFileT(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestStartSessionSendsResumableStart(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)
	assert.Equal(t, g.srv.URL+"/session/obj?upload_id=SESSION_SECRET", uri)

	require.Len(t, g.requests, 1)
	r := g.requests[0]
	assert.Equal(t, http.MethodPost, r.Method)
	assert.Equal(t, "start", r.Resumable)
	assert.Empty(t, r.Auth, "GCS must never see an Authorization header")
}

func TestStartSessionExpiredSignature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	u := testUploader()
	_, err := u.StartSession(context.Background(), srv.URL+"/upload/obj?X-Goog-Signature=SIG")
	require.ErrorIs(t, err, upload.ErrSessionExpired)
	assert.NotContains(t, err.Error(), "SIG", "signed query must not leak into errors")
}

func TestUploadFileSingleChunk(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()
	path := tempFile(t, 1000)

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)
	require.NoError(t, u.UploadFile(context.Background(), uri, path, 1000, 0, nil))

	puts := g.dataPuts()
	require.Len(t, puts, 1)
	assert.Equal(t, "bytes 0-999/1000", puts[0].ContentRange)
	assert.Equal(t, readFileT(t, path), g.object)
	assert.True(t, g.complete)
}

func TestUploadFileChunksAndContentRanges(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()
	size := 2*chunk + 1234
	path := tempFile(t, size)

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)

	var commits []int64
	require.NoError(t, u.UploadFile(context.Background(), uri, path, int64(size), 0,
		func(n int64) { commits = append(commits, n) }))

	puts := g.dataPuts()
	require.Len(t, puts, 3)
	assert.Equal(t, fmt.Sprintf("bytes 0-%d/%d", chunk-1, size), puts[0].ContentRange)
	assert.Equal(t, fmt.Sprintf("bytes %d-%d/%d", chunk, 2*chunk-1, size), puts[1].ContentRange)
	assert.Equal(t, fmt.Sprintf("bytes %d-%d/%d", 2*chunk, size-1, size), puts[2].ContentRange)
	assert.Equal(t, readFileT(t, path), g.object)
	assert.Equal(t, []int64{chunk, 2 * chunk, int64(size)}, commits)
}

func TestUploadFileRetriesFrom500AndNeverResendsAckedBytes(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()
	size := 3 * chunk
	path := tempFile(t, size)

	// Second chunk fails once with 503 before anything of it commits.
	g.failPut[chunk] = []int{503}

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)
	require.NoError(t, u.UploadFile(context.Background(), uri, path, int64(size), 0, nil))

	// fakeGCS.handle asserts every accepted chunk starts exactly at the
	// committed offset; here we additionally pin the observed sequence.
	var ranges []string
	for _, p := range g.dataPuts() {
		ranges = append(ranges, p.ContentRange)
	}
	assert.Equal(t, []string{
		fmt.Sprintf("bytes 0-%d/%d", chunk-1, size),
		fmt.Sprintf("bytes %d-%d/%d", chunk, 2*chunk-1, size), // 503, nothing committed
		fmt.Sprintf("bytes %d-%d/%d", chunk, 2*chunk-1, size), // resumed exactly at committed offset
		fmt.Sprintf("bytes %d-%d/%d", 2*chunk, 3*chunk-1, size),
	}, ranges)
	assert.Equal(t, readFileT(t, path), g.object)
}

func TestUploadFileHonorsPartialServerAck(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()
	size := 2 * chunk
	path := tempFile(t, size)

	// Server acks only half of the first chunk (Range: bytes=0-131071).
	g.ackShort[0] = chunk / 2

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)
	require.NoError(t, u.UploadFile(context.Background(), uri, path, int64(size), 0, nil))

	puts := g.dataPuts()
	require.GreaterOrEqual(t, len(puts), 3)
	// After the short ack the client must continue from the server's
	// committed offset, not its own accounting.
	assert.Equal(t, int64(chunk/2), puts[1].RangeStart)
	assert.Equal(t, readFileT(t, path), g.object)
}

func TestUploadFileResumesFromOffset(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()
	size := 2 * chunk
	path := tempFile(t, size)
	data := readFileT(t, path)

	// Pretend the first chunk was committed in an earlier run.
	g.object = append([]byte(nil), data[:chunk]...)
	g.committed = chunk

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)

	from, done, err := u.QueryOffset(context.Background(), uri, int64(size))
	require.NoError(t, err)
	assert.False(t, done)
	require.Equal(t, int64(chunk), from)

	require.NoError(t, u.UploadFile(context.Background(), uri, path, int64(size), from, nil))
	puts := g.dataPuts()
	require.Len(t, puts, 1, "already-committed bytes must not be re-uploaded")
	assert.Equal(t, int64(chunk), puts[0].RangeStart)
	assert.Equal(t, data, g.object)
}

func TestQueryOffsetVariants(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()
	uri := g.srv.URL + "/session/obj?upload_id=SESSION_SECRET"

	// Nothing committed: 308 without Range header.
	off, done, err := u.QueryOffset(context.Background(), uri, 1000)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Zero(t, off)

	// Fully committed object: 200.
	g.mu.Lock()
	g.complete = true
	g.mu.Unlock()
	_, done, err = u.QueryOffset(context.Background(), uri, 1000)
	require.NoError(t, err)
	assert.True(t, done)
}

func TestUploadFileExpiredSessionSurfacesErrSessionExpired(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()
	path := tempFile(t, 100)
	// GCS answers 404 for unknown/expired session URIs.
	uri := g.srv.URL + "/gone/obj?upload_id=OLD_SECRET"

	err := u.UploadFile(context.Background(), uri, path, 100, 0, nil)
	require.ErrorIs(t, err, upload.ErrSessionExpired)
	assert.NotContains(t, err.Error(), "OLD_SECRET", "session URI must not leak into errors")
}

func TestUploadFileGivesUpAfterMaxRetries(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()
	u.MaxRetries = 2
	path := tempFile(t, 100)
	g.failPut[0] = []int{503, 503, 503, 503, 503}

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)
	err = u.UploadFile(context.Background(), uri, path, 100, 0, nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, upload.ErrSessionExpired)
}

func TestUploadFileContextCanceled(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()
	path := tempFile(t, 100)

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = u.UploadFile(ctx, uri, path, 100, 0, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestUploadFileStallTimeoutIsRetryable(t *testing.T) {
	g := newFakeGCS(t)
	g.hangPut = 200 * time.Millisecond
	u := testUploader()
	u.StallTimeout = 20 * time.Millisecond
	u.MaxRetries = 1
	path := tempFile(t, 100)

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)
	err = u.UploadFile(context.Background(), uri, path, 100, 0, nil)
	require.Error(t, err, "stalled chunks retry and eventually give up")
	require.NotErrorIs(t, err, context.Canceled, "a stall is not a user cancel")
}

func TestTransportErrorsAreRedacted(t *testing.T) {
	g := newFakeGCS(t)
	u := testUploader()
	u.MaxRetries = 0
	path := tempFile(t, 100)

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)
	g.srv.Close() // force a connection error carrying the URL

	err = u.UploadFile(context.Background(), uri, path, 100, 0, nil)
	require.Error(t, err)
	msg := err.Error()
	assert.NotContains(t, msg, "SESSION_SECRET", "resumable session URI is a credential")
	assert.NotContains(t, msg, "upload_id", "query string must be redacted")
}

func TestChunkSizeMustBe256KiBMultiple(t *testing.T) {
	u := testUploader()
	u.ChunkSize = 1000 // not a 256 KiB multiple: rounded up internally
	g := newFakeGCS(t)
	path := tempFile(t, 300*1024)

	uri, err := u.StartSession(context.Background(), g.signedURL())
	require.NoError(t, err)
	require.NoError(t, u.UploadFile(context.Background(), uri, path, 300*1024, 0, nil))
	puts := g.dataPuts()
	require.Len(t, puts, 2)
	assert.Equal(t, int64(chunk), puts[0].BodyLen, "chunks align to 256 KiB")
}
