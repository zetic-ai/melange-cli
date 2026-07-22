package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

const statusPathM1 = "/v1/repos/zetic/whisper/models/m_1/status"

func TestStatusHumanTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", statusPathM1),
		jsonStub(200, `{"state":"converting","terminal":false,"download_ready":false,
			"stage":"convert","created_at":"2026-07-20T10:00:00Z","updated_at":"2026-07-20T10:05:00Z"}`))

	require.NoError(t, run(t, e, "status", "m_1", "-R", repoArg))
	out := e.out.String()
	assert.Contains(t, out, "m_1")
	assert.Contains(t, out, "converting")
	assert.Contains(t, out, "convert")
	assert.Contains(t, out, "no", "download_ready renders as yes/no")
}

func TestStatusTabSeparatedNonTTY(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", statusPathM1),
		jsonStub(200, `{"state":"failed","terminal":true,"download_ready":false,
			"failure_code":"convert_error","created_at":"2026-07-20T10:00:00Z","updated_at":"2026-07-20T10:05:00Z"}`))

	// A plain status read exits 0 even for failed models: it is a query.
	require.NoError(t, run(t, e, "status", "m_1", "-R", repoArg))
	out := e.out.String()
	assert.Contains(t, out, "state\tfailed\n")
	assert.Contains(t, out, "terminal\ttrue\n")
	assert.Contains(t, out, "download_ready\tfalse\n")
	assert.Contains(t, out, "failure_code\tconvert_error\n")
}

func TestStatusJSONRaw(t *testing.T) {
	e := setup(t)
	body := `{"state":"ready","terminal":true,"download_ready":true,"created_at":"2026-07-20T10:00:00Z","updated_at":"2026-07-20T10:05:00Z"}`
	e.reg.Register(httpmock.REST("GET", statusPathM1), jsonStub(200, body))

	require.NoError(t, run(t, e, "status", "m_1", "-R", repoArg, "--json"))
	assert.JSONEq(t, body, e.out.String())
}

func TestStatusRequiresRepo(t *testing.T) {
	e := setup(t)
	err := run(t, e, "status", "m_1")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
}

func TestStatusWaitRequiresPositiveTimeout(t *testing.T) {
	for _, timeout := range []string{"0s", "-1s"} {
		t.Run(timeout, func(t *testing.T) {
			e := setup(t)
			err := run(t, e, "status", "m_1", "-R", repoArg, "--wait", "--timeout", timeout)
			require.Error(t, err)
			assert.Equal(t, 2, cmdutil.ExitCode(err))
			assert.Contains(t, err.Error(), "--timeout must be positive")
			assert.Empty(t, e.reg.Requests, "invalid wait budgets must fail before API access")
		})
	}
}

func TestStatusTimeoutRequiresWait(t *testing.T) {
	e := setup(t)
	err := run(t, e, "status", "m_1", "-R", repoArg, "--timeout", "1m")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--timeout requires --wait")
	assert.Empty(t, e.reg.Requests)
}

func TestStatusWaitUntilTerminalFailedExit1(t *testing.T) {
	e := setup(t)
	fakePoll(t)
	e.reg.Register(httpmock.REST("GET", statusPathM1),
		jsonStub(200, statusBody("converting", false, "")))
	e.reg.Register(httpmock.REST("GET", statusPathM1),
		jsonStub(200, statusBody("failed", true, "convert_error")))

	err := run(t, e, "status", "m_1", "-R", repoArg, "--wait")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err), "--wait makes failed terminal states exit 1")
	assert.Contains(t, e.errOut.String(), "convert_error")
	e.reg.Verify(t)
}

func TestStatusWaitReadyExit0(t *testing.T) {
	e := setup(t)
	fakePoll(t)
	e.reg.Register(httpmock.REST("GET", statusPathM1),
		jsonStub(200, statusBody("converting", false, "")))
	e.reg.Register(httpmock.REST("GET", statusPathM1),
		jsonStub(200, statusBody("ready", true, "")))

	require.NoError(t, run(t, e, "status", "m_1", "-R", repoArg, "--wait"))
	assert.Contains(t, e.out.String(), "ready")
}
