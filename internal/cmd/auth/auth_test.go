package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

const maliciousMeBody = `{
	"user": {"email": "dev\u001b]52;c;VVNFUl9TRUNSRVQ=\u0007@zetic.ai", "nickname": "dev"},
	"account": {"name": "Ze\u001b]52;c;QUNDT1VOVF9TRUNSRVQ=\u0007tic", "type": "org"},
	"token": {
		"name": "ci\u001b]52;c;VE9LRU5fU0VDUkVU\u0007-token",
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

func TestLoginHumanOutputSanitizesServerControlledIdentity(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"),
		httpmock.StatusStringResponse(200, maliciousMeBody))
	e.in.WriteString("ztp_abc123\n")

	require.NoError(t, run(t, e, "auth", "login", "--with-token"))

	output := e.errOut.String()
	assert.Contains(t, output, "as Zetic")
	assert.Contains(t, output, "token: ci-token")
	assert.NotContains(t, output, "\x1b")
	for _, payload := range []string{
		"VVNFUl9TRUNSRVQ", "QUNDT1VOVF9TRUNSRVQ", "VE9LRU5fU0VDUkVU",
	} {
		assert.NotContains(t, output, payload)
	}
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

func TestLoginJQ(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.in.WriteString("ztp_abc123\n")

	err := run(t, e, "auth", "login", "--with-token", "--jq", ".storage")
	require.NoError(t, err)
	assert.Equal(t, "keyring\n", e.out.String())
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

func TestOAuthLoginValidatesBeforeReplacingStoredPAT(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_existing"))

	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
		"revocation_endpoint":    "https://api.zetic.ai/oauth/revoke",
	}
	e.reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(http.StatusOK, discovery))
	e.reg.Register(httpmock.REST(http.MethodPost, "/oauth/register"),
		httpmock.JSONResponse(http.StatusCreated, map[string]string{"client_id": "test_client"}))
	e.reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(http.StatusOK, discovery))
	e.reg.Register(httpmock.REST(http.MethodPost, "/oauth/token"),
		httpmock.JSONResponse(http.StatusOK, map[string]any{
			"access_token": "zoa_rejected", "refresh_token": "zor_rejected",
			"expires_in": 3600, "scope": "write", "token_type": "Bearer",
		}))
	e.reg.Register(httpmock.REST(http.MethodGet, "/v1/me"),
		httpmock.StatusStringResponse(http.StatusUnauthorized, authErrBody))

	var loginOutput synchronizedBuffer
	e.f.IOStreams.ErrOut = &loginOutput
	cmd := root.NewCmdRoot(e.f)
	cmd.SetArgs([]string{"auth", "login", "--no-browser"})
	errCh := make(chan error, 1)
	go func() { errCh <- cmd.ExecuteContext(context.Background()) }()

	var authorizeURL string
	require.Eventually(t, func() bool {
		for _, line := range strings.Split(loginOutput.String(), "\n") {
			if strings.HasPrefix(line, "Opening https://") {
				authorizeURL = strings.TrimPrefix(line, "Opening ")
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	u, err := url.Parse(authorizeURL)
	require.NoError(t, err)
	callback := u.Query().Get("redirect_uri") + "?code=test-code&state=" + url.QueryEscape(u.Query().Get("state"))
	resp, err := http.Get(callback) //nolint:gosec // test-only loopback URL generated by the flow
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rejected the token")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for OAuth login")
	}
	pat, err := keyring.Get("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "ztp_existing", pat)
	_, err = keyring.GetOAuth("api.zetic.ai")
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}

func TestOAuthLoginDoesNotFallbackOnDCRProtocolError(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	prompted := false
	e.f.IOStreams.SetPasswordReader(func(int) ([]byte, error) {
		prompted = true
		return nil, errors.New("unexpected PAT prompt")
	})
	discovery := map[string]string{
		"authorization_endpoint": "https://api.zetic.ai/oauth/authorize",
		"token_endpoint":         "https://api.zetic.ai/oauth/token",
		"registration_endpoint":  "https://api.zetic.ai/oauth/register",
	}
	e.reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, httpmock.JSONResponse(http.StatusOK, discovery))
	e.reg.Register(httpmock.REST(http.MethodPost, "/oauth/register"),
		httpmock.StatusStringResponse(http.StatusInternalServerError, "broken"))

	err := run(t, e, "auth", "login")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DCR failed 500")
	assert.False(t, prompted)
}

func TestOAuthLoginFallsBackOnTransportError(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	prompted := false
	e.f.IOStreams.SetPasswordReader(func(int) ([]byte, error) {
		prompted = true
		return []byte("ztp_fallback"), nil
	})
	e.reg.Register(func(req *http.Request) bool {
		return req.Method == http.MethodGet && strings.Contains(req.URL.Path, ".well-known")
	}, func(*http.Request) (*http.Response, error) { return nil, errors.New("dial failed") })
	e.reg.Register(httpmock.REST(http.MethodPost, "/oauth/register"),
		func(*http.Request) (*http.Response, error) { return nil, errors.New("dial failed") })
	e.reg.Register(httpmock.REST(http.MethodGet, "/v1/me"),
		httpmock.StatusStringResponse(http.StatusOK, meBody))

	err := run(t, e, "auth", "login")
	require.NoError(t, err)
	assert.True(t, prompted)
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
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
	read := false
	e.f.IOStreams.SetPasswordReader(func(fd int) ([]byte, error) {
		read = true
		assert.Equal(t, -1, fd)
		return []byte("ztp_hidden"), nil
	})

	err := run(t, e, "auth", "login")
	require.NoError(t, err)

	assert.True(t, read, "interactive login must use the hidden password reader")
	assert.Contains(t, e.errOut.String(),
		"Paste your personal access token (create one at Settings → Personal Access Tokens):")
	stored, err := keyring.Get("api.zetic.ai")
	require.NoError(t, err)
	assert.Equal(t, "ztp_hidden", stored)
}

func TestLoginHiddenPromptHonorsContextCancellation(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	e.f.IOStreams.SetPasswordReader(func(int) ([]byte, error) {
		entered <- struct{}{}
		<-release
		return nil, io.EOF
	})

	ctx, cancel := context.WithCancel(context.Background())
	cmd := root.NewCmdRoot(e.f)
	cmd.SetIn(e.in)
	cmd.SetOut(e.out)
	cmd.SetErr(e.errOut)
	cmd.SetArgs([]string{"auth", "login"})
	result := make(chan error, 1)
	go func() { result <- cmd.ExecuteContext(ctx) }()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hidden reader was not entered")
	}
	cancel()
	var err error
	select {
	case err = <-result:
	case <-time.After(time.Second):
		t.Fatal("hidden read did not stop after cancellation")
	}
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 130, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests, "canceled credentials must never be verified or stored")
	_, keyErr := keyring.Get("api.zetic.ai")
	assert.ErrorIs(t, keyErr, keyring.ErrNotFound)
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
	assert.Equal(t, config.CredentialStorageConfig, cfg.Hosts["api.zetic.ai"].Storage)

	// The next command must use the explicitly selected config credential
	// without touching the still-unavailable keyring.
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	err = run(t, e, "auth", "status")
	require.NoError(t, err)
	require.Len(t, e.reg.Requests, 2)
	assert.Equal(t, "Bearer ztp_abc123", e.reg.Requests[1].Header.Get("Authorization"))
}

func TestSuccessfulKeyringLoginClearsPriorConfigStorageSelection(t *testing.T) {
	e := setup(t)
	cfg, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, cfg.SetHostAPIKey("api.zetic.ai", "ztp_old_config"))

	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.in.WriteString("ztp_new_keyring\n")
	require.NoError(t, run(t, e, "auth", "login", "--with-token"))

	loaded, err := config.Load()
	require.NoError(t, err)
	_, exists := loaded.Hosts["api.zetic.ai"]
	assert.False(t, exists, "a keyring login must clear the prior config selection")

	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	require.NoError(t, run(t, e, "auth", "status"))
	require.Len(t, e.reg.Requests, 2)
	assert.Equal(t, "Bearer ztp_new_keyring", e.reg.Requests[1].Header.Get("Authorization"))
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
	assert.NotContains(t, e.errOut.String(), "ztp_env")
	assert.NotContains(t, e.errOut.String(), "ztp_abc123")
}

func TestLoginWarnsWhenEnvTokenFileSet(t *testing.T) {
	e := setup(t)
	keyFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(keyFile, []byte("ztp_file_secret\n"), 0600))
	t.Setenv(config.EnvAPIKeyFile, keyFile)
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.in.WriteString("ztp_abc123\n")

	err := run(t, e, "auth", "login", "--with-token", "--json")
	require.NoError(t, err)
	assert.Contains(t, e.errOut.String(), config.EnvAPIKeyFile)
	assert.Contains(t, e.errOut.String(), "takes precedence")
	assert.NotContains(t, e.errOut.String(), keyFile)
	assert.NotContains(t, e.errOut.String(), "ztp_file_secret")
	assert.NotContains(t, e.errOut.String(), "ztp_abc123")

	var got struct {
		Storage string `json:"storage"`
	}
	require.NoError(t, json.Unmarshal(e.out.Bytes(), &got))
	assert.Equal(t, "keyring", got.Storage)
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

func TestStatusHumanSanitizesServerControlledIdentityButJSONPreservesValues(t *testing.T) {
	e := setup(t)
	require.NoError(t, keyring.Set("api.zetic.ai", "ztp_stored"))
	e.reg.Register(httpmock.REST("GET", "/v1/me"),
		httpmock.StatusStringResponse(200, maliciousMeBody))

	require.NoError(t, run(t, e, "auth", "status"))
	human := e.out.String()
	assert.Contains(t, human, "Account: Zetic")
	assert.Contains(t, human, "Token: ci-token")
	assert.NotContains(t, human, "\x1b")
	for _, payload := range []string{
		"VVNFUl9TRUNSRVQ", "QUNDT1VOVF9TRUNSRVQ", "VE9LRU5fU0VDUkVU",
	} {
		assert.NotContains(t, human, payload)
	}

	e.out.Reset()
	e.reg.Register(httpmock.REST("GET", "/v1/me"),
		httpmock.StatusStringResponse(200, maliciousMeBody))
	require.NoError(t, run(t, e, "auth", "status", "--json"))
	var structured struct {
		Account string `json:"account"`
	}
	require.NoError(t, json.Unmarshal(e.out.Bytes(), &structured))
	assert.Contains(t, structured.Account, "\x1b]52;")
	assert.Contains(t, structured.Account, "QUNDT1VOVF9TRUNSRVQ")
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

const planBody = `{"plan":"pro","is_trial":false,"trial_ends_at":null}`

func TestStatusShowsPlanWhenAvailable(t *testing.T) {
	e := setup(t)
	t.Setenv(config.EnvAPIKey, "ztp_env")
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.reg.Register(httpmock.REST("GET", "/v1/billing/plan"), httpmock.StatusStringResponse(200, planBody))

	require.NoError(t, run(t, e, "auth", "status"))
	assert.Contains(t, e.out.String(), "Plan: pro")

	e.out.Reset()
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.reg.Register(httpmock.REST("GET", "/v1/billing/plan"), httpmock.StatusStringResponse(200, planBody))
	require.NoError(t, run(t, e, "auth", "status", "--json"))
	var got struct {
		Plan string `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(e.out.Bytes(), &got))
	assert.Equal(t, "pro", got.Plan)
}

func TestStatusOmitsPlanWhenEndpointMissing(t *testing.T) {
	e := setup(t)
	t.Setenv(config.EnvAPIKey, "ztp_env")
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.reg.Register(httpmock.REST("GET", "/v1/billing/plan"),
		httpmock.StatusStringResponse(404, `{"type":"error","error":{"type":"not_found_error","message":"nope"},"request_id":"r"}`))

	// A missing/failing plan endpoint must not fail status or add a Plan line.
	require.NoError(t, run(t, e, "auth", "status"))
	assert.NotContains(t, e.out.String(), "Plan:")

	e.out.Reset()
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))
	e.reg.Register(httpmock.REST("GET", "/v1/billing/plan"),
		httpmock.StatusStringResponse(404, `{"type":"error","error":{"type":"not_found_error","message":"nope"},"request_id":"r"}`))
	require.NoError(t, run(t, e, "auth", "status", "--json"))
	assert.NotContains(t, e.out.String(), "\"plan\"")
}

func TestStatusJQ(t *testing.T) {
	e := setup(t)
	t.Setenv(config.EnvAPIKey, "ztp_env")
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))

	err := run(t, e, "auth", "status", "--jq", ".account")
	require.NoError(t, err)
	assert.Equal(t, "Zetic\n", e.out.String())
}

func TestStatusTemplate(t *testing.T) {
	e := setup(t)
	t.Setenv(config.EnvAPIKey, "ztp_env")
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, meBody))

	err := run(t, e, "auth", "status", "--template", "{{.host}} {{.token_source}}")
	require.NoError(t, err)
	assert.Equal(t, "api.zetic.ai env:MELANGE_API_KEY", e.out.String())
}

func TestStatusJQTemplateConflictExits2(t *testing.T) {
	e := setup(t)
	t.Setenv(config.EnvAPIKey, "ztp_env")

	err := run(t, e, "auth", "status", "--jq", ".host", "--template", "{{.host}}")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests, "usage errors must not reach the API")
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

func TestLogoutRemovesExplicitConfigCredentialWhenKeyringIsLocked(t *testing.T) {
	e := setup(t)
	cfg, err := config.Load()
	require.NoError(t, err)
	require.NoError(t, cfg.SetHostAPIKey("api.zetic.ai", "ztp_config"))

	locked := errors.New("keychain locked")
	gokeyring.MockInitWithError(locked)

	err = run(t, e, "auth", "logout")
	require.ErrorIs(t, err, locked)
	assert.NotContains(t, e.errOut.String(), "✓ Logged out",
		"a partial storage failure must not be reported as complete success")

	reloaded, loadErr := config.LoadFrom(filepath.Join(config.ConfigDir(), "config.yml"))
	require.NoError(t, loadErr)
	_, ok := reloaded.Hosts["api.zetic.ai"]
	assert.False(t, ok, "the explicitly selected config credential must still be removed")
}

func TestLogoutExampleDoesNotUseHiddenHostFlag(t *testing.T) {
	e := setup(t)
	require.NoError(t, run(t, e, "auth", "logout", "--help"))
	help := e.out.String()
	assert.NotContains(t, help, "--host", "examples must not rely on the hidden --host flag")
	assert.Contains(t, help, "MELANGE_HOST", "host targeting is documented via the env var")
}
