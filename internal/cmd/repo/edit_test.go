package repo_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// ---------------------------------------------------------------------------
// repo edit
// ---------------------------------------------------------------------------

func TestRepoEditPatchBodyHasOnlyProvidedFields(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(200, marshal(t, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "edit", "zetic/whisper-tiny",
		"--description", "New description"))

	require.Len(t, e.reg.Requests, 1)
	body := requestBody(t, e.reg.Requests[0])
	assert.Equal(t, "New description", body["description"])
	assert.NotContains(t, body, "is_private", "unset flags must stay out of the PATCH body")
	assert.NotContains(t, body, "tags")
	assert.NotContains(t, body, "use_case")

	assert.Contains(t, e.errOut.String(), "✓ Updated repository zetic/whisper-tiny")
	assert.Empty(t, e.out.String(), "stdout stays clean without --json")
}

func TestRepoEditAllFieldsAndJSON(t *testing.T) {
	e := setup(t)
	body := marshal(t, whisperRepo())
	e.reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"), jsonStub(200, body))

	require.NoError(t, run(t, e, "repo", "edit", "zetic/whisper-tiny",
		"--description", "d", "--visibility", "public",
		"--use-case", "speech", "--tag", "asr", "--tag", "tiny", "--json"))

	req := requestBody(t, e.reg.Requests[0])
	assert.Equal(t, "d", req["description"])
	assert.Equal(t, false, req["is_private"], "--visibility public maps to is_private false")
	assert.Equal(t, "speech", req["use_case"])
	assert.Equal(t, []any{"asr", "tiny"}, req["tags"], "--tag replaces the full tag set")

	assert.Equal(t, body+"\n", e.out.String(), "--json emits the updated resource byte-exact")
}

func TestRepoEditVisibilityPrivate(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(200, marshal(t, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "edit", "zetic/whisper-tiny", "--visibility", "private"))

	body := requestBody(t, e.reg.Requests[0])
	assert.Equal(t, true, body["is_private"])
	assert.NotContains(t, body, "description")
}

func TestRepoEditEmptyDescriptionClears(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(200, marshal(t, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "edit", "zetic/whisper-tiny", "--description", ""))

	body := requestBody(t, e.reg.Requests[0])
	val, ok := body["description"]
	require.True(t, ok, "an explicitly empty --description must be sent to clear the field")
	assert.Equal(t, "", val)
}

func TestRepoEditWithoutAccountResolvesViaMe(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"), jsonStub(200, meBody))
	e.reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(200, marshal(t, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "edit", "whisper-tiny", "--description", "d"))

	require.Len(t, e.reg.Requests, 2)
	assert.Equal(t, "/v1/me", e.reg.Requests[0].URL.Path)
	assert.Equal(t, "/v1/repos/zetic/whisper-tiny", e.reg.Requests[1].URL.Path)
}

func TestRepoEditNoFlagsExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "edit", "zetic/whisper-tiny")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests, "no request without any edit flag")
}

func TestRepoEditInvalidVisibilityExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "edit", "zetic/whisper-tiny", "--visibility", "internal")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestRepoEditInvalidUseCaseExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "edit", "zetic/whisper-tiny", "--use-case", "gaming")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestRepoEditForbiddenSurfacesServerMessage(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(403, `{"type":"error","error":{"type":"permission_error","message":"only the repository owner can change visibility"},"request_id":"req_9"}`))

	err := run(t, e, "repo", "edit", "zetic/whisper-tiny", "--visibility", "public")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "only the repository owner can change visibility",
		"the server's 403 message must surface verbatim")
}

func TestRepoEditNotFoundExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/nope"),
		jsonStub(404, `{"type":"error","error":{"type":"not_found_error","message":"repository zetic/nope not found"},"request_id":"req_4"}`))

	err := run(t, e, "repo", "edit", "zetic/nope", "--description", "d")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "repository zetic/nope not found")
}

func TestRepoEditUnauthenticatedExits4(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid token"},"request_id":"req_1"}`))

	err := run(t, e, "repo", "edit", "zetic/whisper-tiny", "--description", "d")
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))
}
