package keyring_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gokeyring "github.com/zalando/go-keyring"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/keyring"
)

func TestSetGetDeleteOAuthRoundTrip(t *testing.T) {
	gokeyring.MockInit()
	creds := config.OAuthCredentials{
		AccessToken:  "zoa_test_123",
		RefreshToken: "zor_test_456",
		Expiry:       time.Now().Add(time.Hour).Truncate(time.Second),
		ClientID:     "cid123",
		Scope:        "write",
		TokenType:    "Bearer",
	}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", creds))
	got, err := keyring.GetOAuth("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, creds.AccessToken, got.AccessToken)
	assert.Equal(t, creds.RefreshToken, got.RefreshToken)
	assert.Equal(t, creds.ClientID, got.ClientID)
	assert.WithinDuration(t, creds.Expiry, got.Expiry, time.Second)

	// Overwrite
	creds2 := config.OAuthCredentials{AccessToken: "zoa_2", RefreshToken: "zor_2", Expiry: time.Now().Add(2 * time.Hour), ClientID: "cid2"}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", creds2))
	got2, err := keyring.GetOAuth("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "zoa_2", got2.AccessToken)

	// Delete
	require.NoError(t, keyring.DeleteOAuth("api.zetic.ai"))
	_, err = keyring.GetOAuth("api.zetic.ai")
	assert.ErrorIs(t, err, keyring.ErrNotFound)

	// Delete missing
	err = keyring.DeleteOAuth("missing.example.com")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestGetOAuthMissingReturnsErrNotFound(t *testing.T) {
	gokeyring.MockInit()
	_, err := keyring.GetOAuth("nothing.example.com")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestGetOAuthParseError(t *testing.T) {
	gokeyring.MockInit()
	// Directly store invalid JSON under oauth key to trigger Unmarshal error
	require.NoError(t, gokeyring.Set("melange-cli", "api.zetic.ai.oauth", "not-json{{{"))
	_, err := keyring.GetOAuth("api.zetic.ai")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing oauth credentials")
}

func TestSetOAuthKeyringFailure(t *testing.T) {
	boom := errors.New("keychain locked")
	gokeyring.MockInitWithError(boom)
	creds := config.OAuthCredentials{AccessToken: "zoa", RefreshToken: "zor", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}
	err := keyring.SetOAuth("api.zetic.ai", creds)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "storing oauth credentials")
}

func TestGetOAuthKeyringFailure(t *testing.T) {
	boom := errors.New("read failed")
	gokeyring.MockInitWithError(boom)
	_, err := keyring.GetOAuth("api.zetic.ai")
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "reading oauth credentials")
}

func TestDeleteOAuthKeyringFailure(t *testing.T) {
	boom := errors.New("delete failed")
	gokeyring.MockInit()
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", config.OAuthCredentials{AccessToken: "zoa", RefreshToken: "zor", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}))
	// After MockInitWithError, Delete will fail even for existing
	gokeyring.MockInitWithError(boom)
	err := keyring.DeleteOAuth("api.zetic.ai")
	assert.ErrorIs(t, err, boom)
}

func TestLookupOAuth(t *testing.T) {
	gokeyring.MockInit()
	creds := config.OAuthCredentials{AccessToken: "zoa_lookup", RefreshToken: "zor_lookup", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", creds))
	v, ok, err := keyring.LookupOAuth("api.zetic.ai")
	require.NoError(t, err)
	assert.True(t, ok)
	require.NotNil(t, v)
	assert.Equal(t, "zoa_lookup", v.AccessToken)

	_, ok, err = keyring.LookupOAuth("missing.example.com")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestLookupOAuthPropagatesError(t *testing.T) {
	locked := errors.New("keychain locked")
	gokeyring.MockInitWithError(locked)
	_, _, err := keyring.LookupOAuth("api.zetic.ai")
	assert.ErrorIs(t, err, locked)
}

func TestGetOAuthErrorWrappedNotFound(t *testing.T) {
	// Ensure GetOAuth with MockInitWithError that returns zalando ErrNotFound maps to ErrNotFound
	gokeyring.MockInit()
	_, err := keyring.GetOAuth("ghost")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestDeleteOAuthNotFoundWrapped(t *testing.T) {
	gokeyring.MockInit()
	err := keyring.DeleteOAuth("ghost-oauth")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestOAuthSeparateFromPAT(t *testing.T) {
	gokeyring.MockInit()
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_pat_separate"))
	creds := config.OAuthCredentials{AccessToken: "zoa_sep", RefreshToken: "zor_sep", Expiry: time.Now().Add(time.Hour), ClientID: "cid"}
	require.NoError(t, keyring.SetOAuth("api.zetic.ai", creds))
	pat, err := keyring.Get("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "ztp_pat_separate", pat)
	oa, err := keyring.GetOAuth("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "zoa_sep", oa.AccessToken)
	// Delete OAuth should not delete PAT
	require.NoError(t, keyring.DeleteOAuth("api.zetic.ai"))
	_, err = keyring.GetOAuth("api.zetic.ai")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
	pat2, err := keyring.Get("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "ztp_pat_separate", pat2)
}
