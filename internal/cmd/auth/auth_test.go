package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gokeyring "github.com/zalando/go-keyring"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/keyring"
)

const meBody = `{
	"user": {"email": "dev@zetic.ai", "nickname": "dev"},
	"account": {"name": "Zetic", "type": "org"},
	"token": {
		"name": "ci-token",
		"scopes": ["repo:read", "model:write"],
		"expires_at": "2027-01-01T00:00:00Z",
		"last_used_at": null
	}
}`

const authErrBody = `{"type":"error","error":{"type":"authentication_error","message":"invalid token"},"request_id":"req_1"}`

type testEnv struct {
	f      *cmdutil.Factory
	reg    *httpmock.Registry
	in     *bytes.Buffer
	out    *bytes.Buffer
	errOut *bytes.Buffer
}

// setup isolates the environment: mock keyring, empty env, temp config dir.
func setup(t *testing.T) *testEnv {
	t.Helper()
	gokeyring.MockInit()
	t.Setenv(config.EnvAPIKey, "")
	t.Setenv(config.EnvAPIKeyFile, "")
	t.Setenv(config.EnvHost, "")
	t.Setenv("MELANGE_DEBUG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ios, in, out, errOut := iostreams.Test()
	reg := &httpmock.Registry{}
	return &testEnv{
		f: &cmdutil.Factory{
			IOStreams:     ios,
			Version:       "test",
			HTTPTransport: reg,
			Config:        config.Load,
		},
		reg: reg, in: in, out: out, errOut: errOut,
	}
}

func run(t *testing.T, e *testEnv, args ...string) error {
	t.Helper()
	cmd := root.NewCmdRoot(e.f)
	cmd.SetIn(e.in)
	cmd.SetOut(e.out)
	cmd.SetErr(e.errOut)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

// ---------------------------------------------------------------------------
// login
// ---------------------------------------------------------------------------

func TestLoginWithTokenHappyPath(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.in.WriteString("ztp_abc123\n")

	err := run(t, e, "auth", "login", "--with-token")
	require.NoError(t, err)

	stored, err := keyring.Get("api.zetic.ai")
	require.NoError(t, err, "token should be stored in the keyring under the host key")
	assert.Equal(t, "ztp_abc123", stored)

	msg := e.errOut.String()
	assert.Contains(t, msg, "✓ Logged in to api.zetic.ai as Zetic")
	assert.Contains(t, msg, "token: ci-token")
	assert.Contains(t, msg, "scopes: repo:read, model:write")
	assert.Contains(t, msg, "storage: keyring")
	assert.Empty(t, e.out.String(), "data stream must stay clean without --json")

	// The verify request used the pasted token.
	require.Len(t, e.reg.Requests, 1)
	assert.Equal(t, "Bearer ztp_abc123", e.reg.Requests[0].Header.Get("Authorization"))
}

func TestLoginJSON(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.in.WriteString("ztp_abc123\n")

	err := run(t, e, "auth", "login", "--with-token", "--json")
	require.NoError(t, err)

	var got struct {
		Host    string   `json:"host"`
		Account string   `json:"account"`
		Scopes  []string `json:"scopes"`
		Storage string   `json:"storage"`
	}
	require.NoError(t, json.Unmarshal(e.out.Bytes(), &got))
	assert.Equal(t, "api.zetic.ai", got.Host)
	assert.Equal(t, "Zetic", got.Account)
	assert.Equal(t, []string{"repo:read", "model:write"}, got.Scopes)
	assert.Equal(t, "keyring", got.Storage)
}

func TestLoginBadPrefix(t *testing.T) {
	e := setup(t)
	e.in.WriteString("sk-not-melange\n")

	err := run(t, e, "auth", "login", "--with-token")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "not a Melange personal access token (expected ztp_ prefix)")
	assert.Empty(t, e.reg.Requests, "invalid token must not be sent anywhere")
}

func TestLoginRejectedTokenExits4(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(401, authErrBody))
	e.in.WriteString("ztp_revoked\n")

	err := run(t, e, "auth", "login", "--with-token")
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))

	_, kerr := keyring.Get("api.zetic.ai")
	assert.ErrorIs(t, kerr, keyring.ErrNotFound, "rejected token must not be stored")
}

func TestLoginNonInteractiveWithoutFlagExits2(t *testing.T) {
	e := setup(t) // iostreams.Test: stdin is not a TTY

	err := run(t, e, "auth", "login")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "melange auth login --with-token < token.txt")
	assert.Contains(t, err.Error(), "MELANGE_API_KEY")
}

func TestLoginNoInputFlagExits2(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)

	err := run(t, e, "--no-input", "auth", "login")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
}

func TestLoginPromptsOnTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.in.WriteString("ztp_pasted\n")

	err := run(t, e, "auth", "login")
	require.NoError(t, err)

	assert.Contains(t, e.errOut.String(),
		"Paste your personal access token (create one at Settings → Personal Access Tokens):")
	stored, err := keyring.Get("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "ztp_pasted", stored)
}

func TestLoginKeyringFailureWithoutInsecureStorage(t *testing.T) {
	e := setup(t)
	gokeyring.MockInitWithError(errors.New("keychain locked"))
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.in.WriteString("ztp_abc123\n")

	err := run(t, e, "auth", "login", "--with-token")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--insecure-storage")
	assert.Contains(t, err.Error(), "MELANGE_API_KEY")
	assert.NotContains(t, err.Error(), "ztp_abc123", "token must never appear in errors")

	cfg, cfgErr := config.Load()
	require.NoError(t, cfgErr)
	assert.Empty(t, cfg.Hosts, "token must not silently land in the config file")
}

func TestLoginKeyringFailureWithInsecureStorage(t *testing.T) {
	e := setup(t)
	gokeyring.MockInitWithError(errors.New("keychain locked"))
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.in.WriteString("ztp_abc123\n")

	err := run(t, e, "auth", "login", "--with-token", "--insecure-storage")
	require.NoError(t, err)

	assert.Contains(t, e.errOut.String(), "storage: config")

	cfg, cfgErr := config.Load()
	require.NoError(t, cfgErr)
	assert.Equal(t, "ztp_abc123", cfg.Hosts["api.zetic.ai"].APIKey)
}

func TestLoginWarnsWhenEnvTokenSet(t *testing.T) {
	e := setup(t)
	t.Setenv(config.EnvAPIKey, "ztp_env")
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.in.WriteString("ztp_abc123\n")

	err := run(t, e, "auth", "login", "--with-token")
	require.NoError(t, err)
	assert.Contains(t, e.errOut.String(), "MELANGE_API_KEY")
	assert.Contains(t, e.errOut.String(), "precedence")
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func TestStatusNotLoggedInExits4(t *testing.T) {
	e := setup(t)

	err := run(t, e, "auth", "status")
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "Not logged in")
}

func TestStatusHappyFromKeyring(t *testing.T) {
	e := setup(t)
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_stored"))
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))

	err := run(t, e, "auth", "status")
	require.NoError(t, err)

	out := e.out.String()
	assert.Contains(t, out, "api.zetic.ai")
	assert.Contains(t, out, "Zetic")
	assert.Contains(t, out, "repo:read, model:write")
	assert.Contains(t, out, "ci-token")
	assert.Contains(t, out, "keyring")
	assert.NotContains(t, out, "ztp_stored", "token value must not be printed by status")

	require.Len(t, e.reg.Requests, 1)
	assert.Equal(t, "Bearer ztp_stored", e.reg.Requests[0].Header.Get("Authorization"))
}

func TestStatusInvalidTokenExits4NamingSource(t *testing.T) {
	e := setup(t)
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_stale"))
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(401, authErrBody))

	err := run(t, e, "auth", "status")
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "keyring", "the failing token source must be named")
}

func TestStatusJSON(t *testing.T) {
	e := setup(t)
	t.Setenv(config.EnvAPIKey, "ztp_env")
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))

	err := run(t, e, "auth", "status", "--json")
	require.NoError(t, err)

	var got struct {
		Host        string   `json:"host"`
		Account     string   `json:"account"`
		Scopes      []string `json:"scopes"`
		TokenName   string   `json:"token_name"`
		TokenSource string   `json:"token_source"`
		Storage     string   `json:"storage"`
	}
	require.NoError(t, json.Unmarshal(e.out.Bytes(), &got))
	assert.Equal(t, "api.zetic.ai", got.Host)
	assert.Equal(t, "Zetic", got.Account)
	assert.Equal(t, "env:MELANGE_API_KEY", got.TokenSource)
	assert.Equal(t, "ci-token", got.TokenName)
}

// ---------------------------------------------------------------------------
// token
// ---------------------------------------------------------------------------

func TestTokenPrintsExactlyTokenAndNewline(t *testing.T) {
	e := setup(t)
	t.Setenv(config.EnvAPIKey, "ztp_env")

	err := run(t, e, "auth", "token")
	require.NoError(t, err)
	assert.Equal(t, "ztp_env\n", e.out.String())
}

func TestTokenFromKeyring(t *testing.T) {
	e := setup(t)
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_stored"))

	err := run(t, e, "auth", "token")
	require.NoError(t, err)
	assert.Equal(t, "ztp_stored\n", e.out.String())
}

func TestTokenNoneExits4(t *testing.T) {
	e := setup(t)

	err := run(t, e, "auth", "token")
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))
	assert.Empty(t, e.out.String(), "stdout must stay clean on failure")
}

// ---------------------------------------------------------------------------
// logout
// ---------------------------------------------------------------------------

func TestLogoutRemovesStoredCredentials(t *testing.T) {
	e := setup(t)
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_stored"))
	cfg, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, cfg.SetHostAPIKey("api.zetic.ai", "ztp_stored"))

	err = run(t, e, "auth", "logout")
	require.NoError(t, err)

	_, kerr := keyring.Get("api.zetic.ai")
	assert.ErrorIs(t, kerr, keyring.ErrNotFound)

	reloaded, err := config.LoadFrom(filepath.Join(config.ConfigDir(), "config.yml"))
	require.NoError(t, err)
	_, ok := reloaded.Hosts["api.zetic.ai"]
	assert.False(t, ok, "config credential must be removed")

	assert.Contains(t, e.errOut.String(), "✓ Logged out of api.zetic.ai")
}

func TestLogoutNotesEnvStillWins(t *testing.T) {
	e := setup(t)
	t.Setenv(config.EnvAPIKey, "ztp_env")
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_stored"))

	err := run(t, e, "auth", "logout")
	require.NoError(t, err)
	assert.Contains(t, e.errOut.String(), "MELANGE_API_KEY")
}
