package model

// White-box --wait test: injects the deterministic poll hooks (fakePoll in
// upload_test.go), which are not reachable from the external test package.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

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
