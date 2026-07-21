package upload_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/upload"
)

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// Golden vectors: RFC 3720 appendix B.4 CRC32C test patterns plus the widely
// published "hello world" value, rendered in the GCS convention (base64 of
// the big-endian 4-byte sum). These pin both the polynomial (Castagnoli) and
// the byte order; a little-endian encoding or IEEE CRC32 fails all of them.
func TestDigestFileCRC32CGoldenVectors(t *testing.T) {
	incrementing := make([]byte, 32)
	for i := range incrementing {
		incrementing[i] = byte(i)
	}
	tests := []struct {
		name string
		data []byte
		want string // base64(big-endian crc32c)
	}{
		{"rfc3720 32 zeros", make([]byte, 32), "ipE2qg=="},              // 0x8A9136AA
		{"rfc3720 32 ones", bytes.Repeat([]byte{0xff}, 32), "YqirQw=="}, // 0x62A8AB43
		{"rfc3720 incrementing", incrementing, "Rt15Tg=="},              // 0x46DD794E
		{"hello world", []byte("hello world"), "yZRlqg=="},              // 0xC99465AA
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, "f.bin", tc.data)
			d, err := upload.DigestFile(path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, d.CRC32C)
			assert.Equal(t, int64(len(tc.data)), d.Size)
		})
	}
}

func TestDigestFileSHA256(t *testing.T) {
	path := writeFile(t, "f.bin", []byte("hello world"))
	d, err := upload.DigestFile(path)
	require.NoError(t, err)
	// Well-known sha256("hello world").
	assert.Equal(t, "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9", d.SHA256)
}

func TestDigestFileMissing(t *testing.T) {
	_, err := upload.DigestFile(filepath.Join(t.TempDir(), "nope.bin"))
	require.Error(t, err)
}
