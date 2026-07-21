// Package upload implements the client half of the Melange ingestion v2
// protocol: manifest digesting, persisted upload-session state, and the GCS
// resumable upload protocol over v4 signed URLs.
package upload

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// castagnoli is the CRC32C (Castagnoli) table used by GCS object checksums.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Digest holds the size and content digests of a local file.
type Digest struct {
	Size   int64
	CRC32C string // base64 of the big-endian 4-byte sum (GCS convention)
	SHA256 string // lowercase hex
}

// DigestFile streams path once, computing size, CRC32C, and SHA-256 in a
// single read pass.
func DigestFile(path string) (Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Digest{}, err
	}
	defer f.Close() //nolint:errcheck // read-only

	crc := crc32.New(castagnoli)
	sha := sha256.New()
	n, err := io.Copy(io.MultiWriter(crc, sha), f)
	if err != nil {
		return Digest{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return Digest{
		Size:   n,
		CRC32C: CRC32CBase64(crc.Sum32()),
		SHA256: hex.EncodeToString(sha.Sum(nil)),
	}, nil
}

// CRC32CBase64 renders a CRC32C sum in the GCS convention: base64 of the
// big-endian 4-byte value.
func CRC32CBase64(sum uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], sum)
	return base64.StdEncoding.EncodeToString(b[:])
}
