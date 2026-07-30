// Package plan_test exercises `melange plan` through the full root command so
// persistent flags and exit-code mapping apply.
package plan_test

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

const planPath = "/v1/billing/plan"

const (
	proBody   = `{"plan":"pro","is_trial":false,"trial_ends_at":null}`
	trialBody = `{"plan":"pro_plus","is_trial":true,"trial_ends_at":"2026-08-01T00:00:00Z"}`
	forbidden = `{"type":"error","error":{"type":"permission_error","message":"token lacks access"},"request_id":"req_2"}`
)

func TestPlanTTYNonTrial(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, proBody))

	require.NoError(t, run(t, e, "plan"))
	out := e.out.String()
	assert.Contains(t, out, "Plan:")
	assert.Contains(t, out, "pro")
	assert.Contains(t, out, "Trial:         no")
}

func TestPlanTTYTrialShowsEnd(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, trialBody))

	require.NoError(t, run(t, e, "plan"))
	out := e.out.String()
	assert.Contains(t, out, "pro_plus")
	assert.Contains(t, out, "Trial:         yes (ends 2026-08-01T00:00:00Z)")
}

func TestPlanNonTTYTabSeparated(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, proBody))

	require.NoError(t, run(t, e, "plan"))
	want := "plan\tpro\nis_trial\tfalse\ntrial_ends_at\t\n"
	assert.Equal(t, want, e.out.String())
}

func TestPlanNonTTYTrialEnd(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, trialBody))

	require.NoError(t, run(t, e, "plan"))
	want := "plan\tpro_plus\nis_trial\ttrue\ntrial_ends_at\t2026-08-01T00:00:00Z\n"
	assert.Equal(t, want, e.out.String())
}

func TestPlanJSONByteExact(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, proBody))

	require.NoError(t, run(t, e, "plan", "--json"))
	assert.Equal(t, proBody+"\n", e.out.String())
}

func TestPlanJQField(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, proBody))

	require.NoError(t, run(t, e, "plan", "--jq", ".plan"))
	assert.Equal(t, "pro\n", e.out.String())
}

func TestPlanForbiddenExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(403, forbidden))
	err := run(t, e, "plan")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "token lacks access")
}
