package keyring_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gokeyring "github.com/zalando/go-keyring"
	"github.com/zetic-ai/melange-cli/internal/keyring"
)

func TestSetGetDelete(t *testing.T) {
	gokeyring.MockInit()

	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_secret"))

	got, err := keyring.Get("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "ztp_secret", got)

	require.NoError(t, keyring.Delete("api.zetic.ai"))

	_, err = keyring.Get("api.zetic.ai")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestGetMissingHostReturnsErrNotFound(t *testing.T) {
	gokeyring.MockInit()

	_, err := keyring.Get("nothing.example.com")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestDeleteMissingHostReturnsErrNotFound(t *testing.T) {
	gokeyring.MockInit()

	err := keyring.Delete("nothing.example.com")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestMockInitWithError(t *testing.T) {
	boom := errors.New("keychain locked")
	gokeyring.MockInitWithError(boom)

	err := keyring.Set("api.zetic.ai", "ztp_secret")
	assert.ErrorIs(t, err, boom)
}

func TestLookup(t *testing.T) {
	gokeyring.MockInit()

	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_secret"))

	v, ok := keyring.Lookup("api.zetic.ai")
	assert.True(t, ok)
	assert.Equal(t, "ztp_secret", v)

	_, ok = keyring.Lookup("nothing.example.com")
	assert.False(t, ok)
}

func TestHostKey(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://api.zetic.ai", "api.zetic.ai"},
		{"https://api.zetic.ai/", "api.zetic.ai"},
		{"http://localhost:8080", "localhost:8080"},
		{"https://api.zetic.ai:8443/v1", "api.zetic.ai:8443"},
		{"api.zetic.ai", "api.zetic.ai"},
		{"api.zetic.ai:9000", "api.zetic.ai:9000"},
		{"api.zetic.ai/some/path", "api.zetic.ai"},
		{"  https://api.zetic.ai  ", "api.zetic.ai"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, keyring.HostKey(tt.in))
		})
	}
}
