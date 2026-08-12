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
	// proBody is a legacy-generation account: tier stays null there.
	proBody   = `{"plan":"pro","is_trial":false,"trial_ends_at":null,"billing_generation":"legacy","tier":null,"max_model_bytes":20000000000}`
	trialBody = `{"plan":"pro_plus","is_trial":true,"trial_ends_at":"2026-08-01T00:00:00Z","billing_generation":"legacy","tier":null,"max_model_bytes":20000000000}`
	// v3Body is a Pricing-v3 account: tier is the current pricing identity.
	v3Body    = `{"plan":"pro","is_trial":false,"trial_ends_at":null,"billing_generation":"v3","tier":"team","max_model_bytes":50000000000}`
	forbidden = `{"type":"error","error":{"type":"permission_error","message":"token lacks access"},"request_id":"req_2"}`
)

func TestPlanTTYNonTrial(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, proBody))

	require.NoError(t, run(t, e, "--no-color", "plan"))
	// Labels align to the longest of them, not to a hardcoded column. A
	// legacy-generation account has no tier, so no Tier line renders.
	assert.Equal(t, "Plan:                pro\n"+
		"Trial:               no\n"+
		"Billing generation:  legacy\n"+
		"Max model bytes:     20000000000\n", e.out.String())
}

func TestPlanTTYV3ShowsTier(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, v3Body))

	require.NoError(t, run(t, e, "--no-color", "plan"))
	assert.Equal(t, "Plan:                pro\n"+
		"Trial:               no\n"+
		"Tier:                team\n"+
		"Billing generation:  v3\n"+
		"Max model bytes:     50000000000\n", e.out.String())
}

func TestPlanTTYTrialShowsEnd(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, trialBody))

	require.NoError(t, run(t, e, "--no-color", "plan"))
	assert.Equal(t, "Plan:                pro_plus\n"+
		"Trial:               yes (ends 2026-08-01T00:00:00Z)\n"+
		"Billing generation:  legacy\n"+
		"Max model bytes:     20000000000\n",
		e.out.String())
}

func TestPlanNonTTYTabSeparated(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, proBody))

	require.NoError(t, run(t, e, "plan"))
	// The pricing-v3 identity lines are APPENDED after the historical three;
	// a legacy account's null tier renders as an empty value.
	want := "plan\tpro\nis_trial\tfalse\ntrial_ends_at\t\n" +
		"billing_generation\tlegacy\ntier\t\nmax_model_bytes\t20000000000\n"
	assert.Equal(t, want, e.out.String())
}

func TestPlanNonTTYV3Tier(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, v3Body))

	require.NoError(t, run(t, e, "plan"))
	want := "plan\tpro\nis_trial\tfalse\ntrial_ends_at\t\n" +
		"billing_generation\tv3\ntier\tteam\nmax_model_bytes\t50000000000\n"
	assert.Equal(t, want, e.out.String())
}

func TestPlanNonTTYTrialEnd(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", planPath), jsonStub(200, trialBody))

	require.NoError(t, run(t, e, "plan"))
	want := "plan\tpro_plus\nis_trial\ttrue\ntrial_ends_at\t2026-08-01T00:00:00Z\n" +
		"billing_generation\tlegacy\ntier\t\nmax_model_bytes\t20000000000\n"
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
