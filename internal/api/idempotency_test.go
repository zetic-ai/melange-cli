package api

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uuidV4 is the exact shape every consumer relied on before the helper was
// consolidated here: lowercase hex 8-4-4-4-12 with the version nibble 4 and an
// RFC 4122 variant nibble. The backend deduplicates replays on the exact key
// string, so the format is part of the replay contract — this test is the
// tripwire for anyone reshaping it.
var uuidV4 = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIdempotencyKeyIsUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		key := NewIdempotencyKey()
		assert.Regexp(t, uuidV4, key, "the key format is part of the replay contract")
		assert.False(t, seen[key], "keys must be unique per call: %s", key)
		seen[key] = true
	}
}

// TestNewIdempotencyKeyParamCarriesAFreshKey pins the pointer helper the
// generated params structs take: each call yields a new key (one key per
// LOGICAL operation — reuse across operations would make the backend collapse
// two distinct requests into one).
func TestNewIdempotencyKeyParamCarriesAFreshKey(t *testing.T) {
	first := NewIdempotencyKeyParam()
	second := NewIdempotencyKeyParam()
	require.NotNil(t, first)
	require.NotNil(t, second)
	assert.Regexp(t, uuidV4, string(*first))
	assert.NotEqual(t, string(*first), string(*second),
		"each logical call must mint its own key")
}
