package repo_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// ---------------------------------------------------------------------------
// repo delete
// ---------------------------------------------------------------------------

func TestRepoDeleteNonTTYWithConfirm(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("DELETE", "/v1/repos/zetic/whisper-tiny"),
		httpmock.StatusStringResponse(204, ""))

	require.NoError(t, run(t, e, "repo", "delete", "zetic/whisper-tiny",
		"--confirm", "zetic/whisper-tiny"))

	require.Len(t, e.reg.Requests, 1)
	assert.Contains(t, e.errOut.String(), "✓ Deleted repository zetic/whisper-tiny")
	assert.Empty(t, e.out.String(), "stdout stays clean")
}

func TestRepoDeleteNonTTYWithoutConfirmExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "delete", "zetic/whisper-tiny")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--confirm zetic/whisper-tiny",
		"the error must name the exact flag remediation")
	assert.Empty(t, e.reg.Requests, "no request without confirmation")
}

func TestRepoDeleteConfirmMismatchExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "delete", "zetic/whisper-tiny", "--confirm", "zetic/whisper")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests, "a mismatched --confirm must never reach the API")
}

func TestRepoDeleteBareNameExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "delete", "whisper-tiny", "--confirm", "whisper-tiny")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "ACCOUNT/NAME",
		"destructive commands must demand the full repository path")
	assert.Empty(t, e.reg.Requests, "no default-account resolution on destructive commands")
}

func TestRepoDeleteTTYTypedConfirmation(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	e.in.WriteString("zetic/whisper-tiny\n")
	e.reg.Register(httpmock.REST("DELETE", "/v1/repos/zetic/whisper-tiny"),
		httpmock.StatusStringResponse(204, ""))

	require.NoError(t, run(t, e, "repo", "delete", "zetic/whisper-tiny"))

	require.Len(t, e.reg.Requests, 1)
	assert.Contains(t, e.errOut.String(), "Type zetic/whisper-tiny to confirm")
	assert.Contains(t, e.errOut.String(), "✓ Deleted repository zetic/whisper-tiny")
}

func TestRepoDeleteTTYTypedMismatchRejected(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	e.in.WriteString("zetic/other\n")

	err := run(t, e, "repo", "delete", "zetic/whisper-tiny")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "not deleted")
	assert.Empty(t, e.reg.Requests, "a mismatched confirmation must never reach the API")
}

func TestRepoDeleteNoInputRequiresConfirmFlag(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true) // interactive terminal, but --no-input wins

	err := run(t, e, "--no-input", "repo", "delete", "zetic/whisper-tiny")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

type cancelBlockingReader struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (r cancelBlockingReader) Read([]byte) (int, error) {
	r.entered <- struct{}{}
	<-r.release
	return 0, io.EOF
}

func TestRepoDeleteInteractiveConfirmationHonorsContextCancellation(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdinTTY(true)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	reader := cancelBlockingReader{entered: entered, release: release}
	e.f.IOStreams.In = reader

	ctx, cancel := context.WithCancel(context.Background())
	cmd := root.NewCmdRoot(e.f)
	cmd.SetIn(reader)
	cmd.SetOut(e.out)
	cmd.SetErr(e.errOut)
	cmd.SetArgs([]string{"repo", "delete", "zetic/whisper-tiny"})
	result := make(chan error, 1)
	go func() { result <- cmd.ExecuteContext(ctx) }()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("confirmation reader was not entered")
	}
	cancel()
	var err error
	select {
	case err = <-result:
	case <-time.After(time.Second):
		t.Fatal("confirmation did not stop after cancellation")
	}
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 130, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests, "cancellation at confirmation must never delete the repository")
}

func TestRepoDeleteNotFoundExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("DELETE", "/v1/repos/zetic/nope"),
		jsonStub(404, `{"type":"error","error":{"type":"not_found_error","message":"repository zetic/nope not found"},"request_id":"req_4"}`))

	err := run(t, e, "repo", "delete", "zetic/nope", "--confirm", "zetic/nope")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "repository zetic/nope not found")
}

func TestRepoDeleteForbiddenExits1WithServerMessage(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("DELETE", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(403, `{"type":"error","error":{"type":"permission_error","message":"only the repository owner can delete it"},"request_id":"req_9"}`))

	err := run(t, e, "repo", "delete", "zetic/whisper-tiny", "--confirm", "zetic/whisper-tiny")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "only the repository owner can delete it")
}

func TestRepoDeleteUnauthenticatedExits4(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("DELETE", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(401, `{"type":"error","error":{"type":"authentication_error","message":"invalid token"},"request_id":"req_1"}`))

	err := run(t, e, "repo", "delete", "zetic/whisper-tiny", "--confirm", "zetic/whisper-tiny")
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))
}
