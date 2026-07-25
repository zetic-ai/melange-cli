// Package usage_test exercises `melange usage` and `melange usage quotas`
// through the full root command so persistent flags and exit-code mapping
// apply.
package usage_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

type testEnv struct {
	f      *cmdutil.Factory
	reg    *httpmock.Registry
	in     *bytes.Buffer
	out    *bytes.Buffer
	errOut *bytes.Buffer
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	t.Setenv("MELANGE_DEBUG", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("NO_COLOR", "")

	ios, in, out, errOut := iostreams.Test()
	reg := &httpmock.Registry{}
	f := &cmdutil.Factory{IOStreams: ios, Version: "test", HTTPTransport: reg}
	f.ApiClient = func() (*api.Client, error) {
		return cmdutil.NewAPIClient(f, "https://api.zetic.ai", "ztp_test")
	}
	return &testEnv{f: f, reg: reg, in: in, out: out, errOut: errOut}
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

func jsonStub(status int, body string) httpmock.Responder {
	return httpmock.WithHeader(httpmock.StatusStringResponse(status, body), "Content-Type", "application/json")
}

const (
	usagePath  = "/v1/usage"
	quotasPath = "/v1/usage/quotas"
	forbidden  = `{"type":"error","error":{"type":"permission_error","message":"token lacks access"},"request_id":"req_2"}`
)

// ---------------------------------------------------------------------------
// usage
// ---------------------------------------------------------------------------

func TestUsageBlockTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", usagePath),
		jsonStub(200, `{"active_devices":3,"bandwidth":1024,"model_uploads":7,"prompts":42}`))

	require.NoError(t, run(t, e, "usage"))
	out := e.out.String()
	assert.Contains(t, out, "Active devices:")
	assert.Contains(t, out, "3")
	assert.Contains(t, out, "Prompts:")
	assert.Contains(t, out, "42")
}

func TestUsageNonTTYTabSeparated(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", usagePath),
		jsonStub(200, `{"active_devices":3,"bandwidth":1024,"model_uploads":7,"prompts":42}`))

	require.NoError(t, run(t, e, "usage"))
	want := "active_devices\t3\nbandwidth\t1024\nmodel_uploads\t7\nprompts\t42\n"
	assert.Equal(t, want, e.out.String())
}

func TestUsageJSONByteExact(t *testing.T) {
	e := setup(t)
	body := `{"active_devices":3,"bandwidth":1024,"model_uploads":7,"prompts":42}`
	e.reg.Register(httpmock.REST("GET", usagePath), jsonStub(200, body))

	require.NoError(t, run(t, e, "usage", "--json"))
	assert.Equal(t, body+"\n", e.out.String())
}

func TestUsageForbiddenExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", usagePath), jsonStub(403, forbidden))
	err := run(t, e, "usage")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "token lacks access")
}

// ---------------------------------------------------------------------------
// usage quotas
// ---------------------------------------------------------------------------

// quotasBody: prompts has a limit (50% used), active_devices is unlimited
// (null limit + remaining), model_uploads is a zero-limit edge (renders 0%).
// remaining rides along in --json but the human/non-TTY forms ignore it.
const quotasBody = `{"active_devices":{"used":3,"limit":null,"remaining":null},` +
	`"bandwidth":{"used":500,"limit":1000,"remaining":500},` +
	`"model_uploads":{"used":0,"limit":0,"remaining":0},` +
	`"prompts":{"used":25,"limit":50,"remaining":25}}`

func TestUsageQuotasTTYUnlimitedAndPct(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", quotasPath), jsonStub(200, quotasBody))

	require.NoError(t, run(t, e, "usage", "quotas"))
	out := e.out.String()
	assert.Contains(t, out, "Active devices:  unlimited", "null limit renders unlimited")
	assert.Contains(t, out, "Bandwidth:       500/1000 (50%)")
	assert.Contains(t, out, "Model uploads:   0/0 (0%)", "zero limit does not divide by zero")
	assert.Contains(t, out, "Prompts:         25/50 (50%)")
}

func TestUsageQuotasNonTTYTabSeparated(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", quotasPath), jsonStub(200, quotasBody))

	require.NoError(t, run(t, e, "usage", "quotas"))
	want := "active_devices\tunlimited\n" +
		"bandwidth\t500/1000 (50%)\n" +
		"model_uploads\t0/0 (0%)\n" +
		"prompts\t25/50 (50%)\n"
	assert.Equal(t, want, e.out.String())
}

func TestUsageQuotasJSONByteExact(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", quotasPath), jsonStub(200, quotasBody))

	require.NoError(t, run(t, e, "usage", "quotas", "--json"))
	assert.Equal(t, quotasBody+"\n", e.out.String())
}

func TestUsageQuotasForbiddenExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", quotasPath), jsonStub(403, forbidden))
	err := run(t, e, "usage", "quotas")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
}
