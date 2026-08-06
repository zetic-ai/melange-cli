package api

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// NewIdempotencyKey returns a random UUIDv4. Sent as Idempotency-Key on
// replay-safe mutations (per ADR-5: create/complete/reissue/cancel of upload
// sessions, imports, download authorizations) so this package's retry
// transport may safely replay them: retryEligible treats any request
// carrying an Idempotency-Key header as replay-safe.
//
// The key format is part of the replay contract with the backend and must not
// change: the server deduplicates on the exact key string, so one logical
// operation retried after a 5xx must present the same bytes.
func NewIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; fall back to a
		// timestamp key rather than abandoning the operation over it.
		return fmt.Sprintf("melange-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewIdempotencyKeyParam returns a fresh key as the pointer type the generated
// params structs take. The key is generated once per logical call, so the
// retry transport replays the same key on 5xx retries instead of starting a
// second operation or charging twice.
func NewIdempotencyKeyParam() *gen.IdempotencyKey {
	k := gen.IdempotencyKey(NewIdempotencyKey())
	return &k
}
