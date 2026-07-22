package model

// White-box --wait test: injects the deterministic poll hooks (fakePoll in
// upload_test.go), which are not reachable from the external test package.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

func TestImportWaitRejectsZeroTimeoutBeforeCreation(t *testing.T) {
	e := setup(t)
	err := run(t, e, "import", "org/model", "-R", repoArg, "--wait", "--timeout", "0s")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests, "invalid wait budgets must not create an import")
}

func TestImportWaitPollsUntilReady(t *testing.T) {
	e := setup(t)
	fakePoll(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/import"),
		jsonStub(201, `{"key":"m_hf1","version":1,"state":"converting"}`))
	statusPath := "/v1/repos/zetic/whisper/models/m_hf1/status"
	e.reg.Register(httpmock.REST("GET", statusPath), jsonStub(200, statusBody("converting", false, "")))
	e.reg.Register(httpmock.REST("GET", statusPath), jsonStub(200, statusBody("ready", true, "")))

	require.NoError(t, run(t, e, "import", "meta-llama/Llama-3.2-1B", "-R", repoArg, "--wait"))
	e.reg.Verify(t)
	assert.Contains(t, e.out.String(), "state\tready")
}

func TestImportWaitFailedExits1(t *testing.T) {
	e := setup(t)
	fakePoll(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/import"),
		jsonStub(201, `{"key":"m_hf1","version":1,"state":"converting"}`))
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/m_hf1/status"),
		jsonStub(200, statusBody("failed", true, "unsupported_architecture")))

	err := run(t, e, "import", "meta-llama/Llama-3.2-1B", "-R", repoArg, "--wait")
	require.Error(t, err)
	assert.Contains(t, e.errOut.String(), "unsupported_architecture")
}

func TestImportWaitJSONPreservesImportAndFinalStatus(t *testing.T) {
	e := setup(t)
	fakePoll(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/import"),
		jsonStub(201, `{"key":"m_hf1","version":1,"state":"converting"}`))
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/m_hf1/status"),
		jsonStub(200, statusBody("ready", true, "")))

	require.NoError(t, run(t, e, "import", "meta-llama/Llama-3.2-1B", "-R", repoArg, "--wait", "--json"))
	var got struct {
		Model  gen.ImportModelResponse `json:"model"`
		Status gen.ModelStatusResponse `json:"status"`
	}
	require.NoError(t, json.Unmarshal(e.out.Bytes(), &got))
	assert.Equal(t, "m_hf1", got.Model.Key)
	assert.Equal(t, gen.ModelStatusResponseStateReady, got.Status.State)
	assert.True(t, got.Status.Terminal)
	e.reg.Verify(t)
}

func TestImportWaitJQCanSelectModelKey(t *testing.T) {
	e := setup(t)
	fakePoll(t)

	e.reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper/models/import"),
		jsonStub(201, `{"key":"m_hf1","version":1,"state":"converting"}`))
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper/models/m_hf1/status"),
		jsonStub(200, statusBody("ready", true, "")))

	require.NoError(t, run(t, e, "import", "meta-llama/Llama-3.2-1B", "-R", repoArg,
		"--wait", "--jq", ".model.key"))
	assert.Equal(t, "m_hf1\n", e.out.String())
}
