package oauth

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPKCE(t *testing.T) {
	verifier, err := generateVerifier()
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^[A-Za-z0-9_-]{43}\z`), verifier)
	challenge := challengeFromVerifier(verifier)
	assert.Regexp(t, regexp.MustCompile(`^[A-Za-z0-9_-]{43}\z`), challenge)
	// verifier must be 43 chars (32 bytes base64url no pad)
	assert.Len(t, verifier, 43)
	assert.Len(t, challenge, 43)
}

func TestLoopbackHostRejection(t *testing.T) {
	assert.True(t, isLoopback("127.0.0.1"))
	assert.True(t, isLoopback("localhost"))
	assert.True(t, isLoopback("::1"))
	assert.False(t, isLoopback("evil.example"))
	assert.False(t, isLoopback("192.168.1.1"))
	// Direct isLoopback with host:port should fail (needs SplitHostPort)
	assert.False(t, isLoopback("127.0.0.1:1234"), "isLoopback should be called after SplitHostPort, not with port")
}
