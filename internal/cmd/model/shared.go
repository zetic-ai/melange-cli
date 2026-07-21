package model

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// Poll hooks: nil selects the real jitter/sleeper/clock in internal/wait.
// Tests inject deterministic implementations.
var (
	pollJitter func(time.Duration) time.Duration
	pollSleep  func(context.Context, time.Duration) error
	pollNow    func() time.Time
)

// genClient returns the generated API client over the authenticated
// transport chain.
func genClient(f *cmdutil.Factory) (*gen.ClientWithResponses, error) {
	client, err := f.ApiClient()
	if err != nil {
		return nil, err
	}
	return client.Gen()
}

// bareHTTPClient returns a client with NO melange transport chain: model
// bytes go to GCS signed URLs, which must never see the PAT and must never
// pass through debug logging (signed URLs and resumable session URIs are
// credentials). Only the test transport override is honored.
func bareHTTPClient(f *cmdutil.Factory) *http.Client {
	rt := f.HTTPTransport
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &http.Client{Transport: rt}
}

// splitRepoFlag parses the required -R/--repo value. Model commands never
// fall back to a default repository: uploads are costly, targeted writes.
func splitRepoFlag(value string) (account, name string, err error) {
	if value == "" {
		return "", "", cmdutil.FlagError{Err: errors.New(
			"-R/--repo is required for model commands (no default is applied); pass -R ACCOUNT/REPO")}
	}
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", cmdutil.FlagError{Err: fmt.Errorf(
			"invalid --repo %q; expected ACCOUNT/REPO", value)}
	}
	return parts[0], parts[1], nil
}

// newIdempotencyKey returns a random UUIDv4. Sent as Idempotency-Key on
// create/complete so the api retry transport may safely replay them.
func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; fall back to a
		// timestamp key rather than aborting an upload over it.
		return fmt.Sprintf("melange-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// withIdempotencyKey sets the Idempotency-Key header on a generated request.
func withIdempotencyKey(key string) gen.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Idempotency-Key", key)
		return nil
	}
}

// canceledSilently maps to exit 130 (context.Canceled) while suppressing the
// runner's generic error line (ErrSilent): the command already printed the
// interrupt message and resume hint.
type canceledSilently struct{}

func (canceledSilently) Error() string { return "interrupted" }

func (canceledSilently) Is(target error) bool {
	return target == context.Canceled || target == cmdutil.ErrSilent
}

// deref returns "" for nil string pointers.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
