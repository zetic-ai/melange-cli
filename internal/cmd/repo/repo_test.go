package repo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

const meBody = `{
	"user": {"email": "dev@zetic.ai", "nickname": "dev"},
	"account": {"name": "zetic", "type": "org"},
	"token": {"name": "ci-token", "scopes": ["read", "write"]}
}`

// testNow anchors relative timestamps; truncated to seconds so RFC3339
// round-trips byte-exact through fixtures and command output.
var testNow = time.Now().UTC().Truncate(time.Second)

func strPtr(s string) *string { return &s }

func whisperRepo() gen.RepoResponse {
	return gen.RepoResponse{
		Account:     "zetic",
		Name:        "whisper-tiny",
		FullName:    "zetic/whisper-tiny",
		Description: strPtr("Tiny Whisper for on-device ASR"),
		IsPrivate:   true,
		ModelType:   "general",
		UseCase:     strPtr("speech"),
		Tags:        []string{"asr", "tiny"},
		CreatedAt:   time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC),
		UpdatedAt:   testNow.Add(-3 * time.Hour),
	}
}

func detrRepo() gen.RepoResponse {
	return gen.RepoResponse{
		Account:   "acme",
		Name:      "detr",
		FullName:  "acme/detr",
		IsPrivate: false,
		ModelType: "llm",
		Tags:      []string{},
		CreatedAt: time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC),
		UpdatedAt: testNow.Add(-48 * time.Hour),
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// page builds the paginated envelope exactly as the API returns it.
func page(t *testing.T, count int, repos ...gen.RepoResponse) string {
	t.Helper()
	results := make([]json.RawMessage, len(repos))
	for i, r := range repos {
		results[i] = json.RawMessage(marshal(t, r))
	}
	return marshal(t, struct {
		Results []json.RawMessage `json:"results"`
		Count   int               `json:"count"`
	}{Results: results, Count: count})
}

func jsonStub(status int, body string) httpmock.Responder {
	return httpmock.JSONResponse(status, json.RawMessage(body))
}

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
	f := &cmdutil.Factory{
		IOStreams:     ios,
		Version:       "test",
		HTTPTransport: reg,
	}
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

func requestBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	require.NotNil(t, req.GetBody, "request must expose a replayable body")
	rc, err := req.GetBody()
	require.NoError(t, err)
	defer rc.Close() //nolint:errcheck
	raw, err := io.ReadAll(rc)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

// ---------------------------------------------------------------------------
// repo (parent)
// ---------------------------------------------------------------------------

func TestRepoHelpListsSubcommands(t *testing.T) {
	e := setup(t)
	require.NoError(t, run(t, e, "repo", "--help"))
	help := e.out.String()
	assert.Contains(t, help, "list")
	assert.Contains(t, help, "view")
	assert.Contains(t, help, "create")
}

// ---------------------------------------------------------------------------
// repo list
// ---------------------------------------------------------------------------

func TestRepoListTableTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"),
		jsonStub(200, page(t, 2, whisperRepo(), detrRepo())))

	require.NoError(t, run(t, e, "--no-color", "repo", "list"))

	want := "REPO                VISIBILITY  TYPE     UPDATED\n" +
		"zetic/whisper-tiny  private     general  3h ago\n" +
		"acme/detr           public      llm      2d ago\n"
	assert.Equal(t, want, e.out.String())

	require.Len(t, e.reg.Requests, 1)
	assert.Equal(t, "30", e.reg.Requests[0].URL.Query().Get("limit"),
		"default --limit is 30")
}

func TestRepoListPrivateColoredYellowOnTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	t.Setenv("CLICOLOR_FORCE", "1")
	e.reg.Register(httpmock.REST("GET", "/v1/repos"),
		jsonStub(200, page(t, 1, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "list"))
	assert.Contains(t, e.out.String(), "\x1b[33mprivate\x1b[0m")
}

func TestRepoListNonTTYTabSeparated(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"),
		jsonStub(200, page(t, 2, whisperRepo(), detrRepo())))

	require.NoError(t, run(t, e, "repo", "list"))

	want := fmt.Sprintf("zetic/whisper-tiny\tprivate\tgeneral\t%s\n", testNow.Add(-3*time.Hour).Format(time.RFC3339)) +
		fmt.Sprintf("acme/detr\tpublic\tllm\t%s\n", testNow.Add(-48*time.Hour).Format(time.RFC3339))
	assert.Equal(t, want, e.out.String())
	assert.Empty(t, e.errOut.String())
}

func TestRepoListSearchAndLimitFlags(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"),
		jsonStub(200, page(t, 1, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "list", "--search", "whisper", "--limit", "5"))

	require.Len(t, e.reg.Requests, 1)
	q := e.reg.Requests[0].URL.Query()
	assert.Equal(t, "whisper", q.Get("search"))
	assert.Equal(t, "5", q.Get("limit"))
}

func TestRepoListInvalidLimitExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "list", "--limit", "0")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestRepoListEmptyTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"), jsonStub(200, page(t, 0)))

	require.NoError(t, run(t, e, "--no-color", "repo", "list"))
	assert.Empty(t, e.out.String(), "stdout stays clean")
	assert.Contains(t, e.errOut.String(), "No repositories found")
}

func TestRepoListEmptyNonTTY(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"), jsonStub(200, page(t, 0)))

	require.NoError(t, run(t, e, "repo", "list"))
	assert.Empty(t, e.out.String())
	assert.Empty(t, e.errOut.String())
}

func TestRepoListEmptyJSONStillEmitsEnvelope(t *testing.T) {
	e := setup(t)
	body := page(t, 0)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"), jsonStub(200, body))

	require.NoError(t, run(t, e, "repo", "list", "--json"))
	assert.Equal(t, body+"\n", e.out.String())
}

func TestRepoListJSONPassthrough(t *testing.T) {
	e := setup(t)
	body := page(t, 2, whisperRepo(), detrRepo())
	e.reg.Register(httpmock.REST("GET", "/v1/repos"), jsonStub(200, body))

	require.NoError(t, run(t, e, "repo", "list", "--json"))
	assert.Equal(t, body+"\n", e.out.String(),
		"--json must emit the page envelope exactly as the API returned it")
}

func TestRepoListJQ(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"),
		jsonStub(200, page(t, 2, whisperRepo(), detrRepo())))

	require.NoError(t, run(t, e, "repo", "list", "--jq", ".results[].full_name"))
	assert.Equal(t, "zetic/whisper-tiny\nacme/detr\n", e.out.String())
}

func TestRepoListTemplate(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"),
		jsonStub(200, page(t, 2, whisperRepo(), detrRepo())))

	require.NoError(t, run(t, e, "repo", "list",
		"--template", "{{range .results}}{{tablerow .full_name .model_type}}{{end}}"))
	assert.Equal(t, "zetic/whisper-tiny\tgeneral\nacme/detr\tllm\n", e.out.String())
}

func TestRepoListJQTemplateConflictExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "list", "--jq", ".count", "--template", "{{.count}}")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestRepoListPaginateMergesPages(t *testing.T) {
	e := setup(t)
	r1, r2, r3 := whisperRepo(), detrRepo(), whisperRepo()
	r3.Name = "whisper-large"
	r3.FullName = "zetic/whisper-large"
	e.reg.Register(httpmock.REST("GET", "/v1/repos"), jsonStub(200, page(t, 3, r1, r2)))
	e.reg.Register(httpmock.REST("GET", "/v1/repos"), jsonStub(200, page(t, 3, r3)))

	require.NoError(t, run(t, e, "repo", "list", "--paginate", "--json"))

	assert.Equal(t, page(t, 3, r1, r2, r3)+"\n", e.out.String(),
		"merged envelope keeps the server count")

	require.Len(t, e.reg.Requests, 2)
	q1 := e.reg.Requests[0].URL.Query()
	assert.Equal(t, "100", q1.Get("limit"), "pagination uses the server page size")
	assert.Equal(t, "0", q1.Get("offset"))
	q2 := e.reg.Requests[1].URL.Query()
	assert.Equal(t, "100", q2.Get("limit"))
	assert.Equal(t, "2", q2.Get("offset"), "second page starts after the merged results")
	e.reg.Verify(t)
}

func TestRepoListAllIsPaginateAlias(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"), jsonStub(200, page(t, 1, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "list", "--all", "--json"))
	require.Len(t, e.reg.Requests, 1)
	assert.Equal(t, "100", e.reg.Requests[0].URL.Query().Get("limit"))
}

func TestRepoListPaginateWithLimitExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "list", "--paginate", "--limit", "5")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

// ---------------------------------------------------------------------------
// repo view
// ---------------------------------------------------------------------------

func TestRepoViewTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(200, marshal(t, whisperRepo())))

	require.NoError(t, run(t, e, "--no-color", "repo", "view", "zetic/whisper-tiny"))

	want := "zetic/whisper-tiny\n" +
		"\n" +
		"Visibility:  private\n" +
		"Type:        general\n" +
		"Use case:    speech\n" +
		"Tags:        asr, tiny\n" +
		"Created:     Feb 1, 2026\n" +
		"Updated:     3h ago\n" +
		"\n" +
		"Tiny Whisper for on-device ASR\n"
	assert.Equal(t, want, e.out.String())
	require.Len(t, e.reg.Requests, 1)
}

func TestRepoViewNonTTYKeyValueLines(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(200, marshal(t, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "view", "zetic/whisper-tiny"))

	want := "name\tzetic/whisper-tiny\n" +
		"visibility\tprivate\n" +
		"type\tgeneral\n" +
		"use_case\tspeech\n" +
		"tags\tasr,tiny\n" +
		"description\tTiny Whisper for on-device ASR\n" +
		"created_at\t2026-02-01T09:00:00Z\n" +
		fmt.Sprintf("updated_at\t%s\n", testNow.Add(-3*time.Hour).Format(time.RFC3339))
	assert.Equal(t, want, e.out.String())
}

func TestRepoViewJSON(t *testing.T) {
	e := setup(t)
	body := marshal(t, whisperRepo())
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny"), jsonStub(200, body))

	require.NoError(t, run(t, e, "repo", "view", "zetic/whisper-tiny", "--json"))
	assert.Equal(t, body+"\n", e.out.String())
}

func TestRepoViewWithoutAccountResolvesViaMe(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"), jsonStub(200, meBody))
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(200, marshal(t, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "view", "whisper-tiny"))

	require.Len(t, e.reg.Requests, 2, "exactly one extra call to /v1/me")
	assert.Equal(t, "/v1/me", e.reg.Requests[0].URL.Path)
	assert.Equal(t, "/v1/repos/zetic/whisper-tiny", e.reg.Requests[1].URL.Path)
	assert.Contains(t, e.out.String(), "zetic/whisper-tiny")
}

func TestRepoViewWithAccountSkipsMe(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny"),
		jsonStub(200, marshal(t, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "view", "zetic/whisper-tiny"))
	require.Len(t, e.reg.Requests, 1, "no /v1/me call when the account is explicit")
}

func TestRepoViewNotFoundExits1WithServerMessage(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/nope"),
		jsonStub(404, `{"type":"error","error":{"type":"not_found_error","message":"repository zetic/nope not found"},"request_id":"req_4"}`))

	err := run(t, e, "repo", "view", "zetic/nope")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "repository zetic/nope not found")
	assert.Empty(t, e.out.String())
}

func TestRepoViewMalformedArgExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "view", "a/b/c")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

// ---------------------------------------------------------------------------
// repo create
// ---------------------------------------------------------------------------

func TestRepoCreateHappy(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos"),
		jsonStub(201, marshal(t, whisperRepo())))

	require.NoError(t, run(t, e, "repo", "create", "whisper-tiny"))

	require.Len(t, e.reg.Requests, 1)
	body := requestBody(t, e.reg.Requests[0])
	assert.Equal(t, "whisper-tiny", body["name"])
	assert.Equal(t, "general", body["model_type"], "--model-type defaults to general")
	assert.NotContains(t, body, "description")
	assert.NotContains(t, body, "tags")

	assert.Contains(t, e.errOut.String(), "✓ Created repository zetic/whisper-tiny")
	assert.Empty(t, e.out.String(), "stdout stays clean without --json")
}

func TestRepoCreateWithFlagsAndJSON(t *testing.T) {
	e := setup(t)
	body := marshal(t, whisperRepo())
	e.reg.Register(httpmock.REST("POST", "/v1/repos"), jsonStub(201, body))

	require.NoError(t, run(t, e, "repo", "create", "whisper-tiny",
		"--model-type", "general",
		"--description", "Tiny Whisper for on-device ASR",
		"--tag", "asr", "--tag", "tiny",
		"--json"))

	req := requestBody(t, e.reg.Requests[0])
	assert.Equal(t, "Tiny Whisper for on-device ASR", req["description"])
	assert.Equal(t, []any{"asr", "tiny"}, req["tags"])

	assert.Equal(t, body+"\n", e.out.String(), "--json emits the created resource")
}

func TestRepoCreatePrivateAndUseCaseComposePatch(t *testing.T) {
	e := setup(t)
	created := whisperRepo()
	created.IsPrivate = false
	created.UseCase = nil
	patched := marshal(t, whisperRepo())
	e.reg.Register(httpmock.REST("POST", "/v1/repos"), jsonStub(201, marshal(t, created)))
	e.reg.Register(httpmock.REST("PATCH", "/v1/repos/zetic/whisper-tiny"), jsonStub(200, patched))

	require.NoError(t, run(t, e, "repo", "create", "whisper-tiny",
		"--private", "--use-case", "speech", "--json"))

	require.Len(t, e.reg.Requests, 2)
	patch := requestBody(t, e.reg.Requests[1])
	assert.Equal(t, true, patch["is_private"])
	assert.Equal(t, "speech", patch["use_case"])

	assert.Equal(t, patched+"\n", e.out.String(), "the final resource reflects the patch")
	e.reg.Verify(t)
}

func TestRepoCreateConflictExits1WithServerMessage(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos"),
		jsonStub(409, `{"type":"error","error":{"type":"invalid_request_error","message":"repository whisper-tiny already exists"},"request_id":"req_2"}`))

	err := run(t, e, "repo", "create", "whisper-tiny")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "repository whisper-tiny already exists")
}

func TestRepoCreateForbiddenHintsAtScopes(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos"),
		jsonStub(403, `{"type":"error","error":{"type":"permission_error","message":"token lacks the write scope"},"request_id":"req_3"}`))

	err := run(t, e, "repo", "create", "whisper-tiny")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "token lacks the write scope")
	assert.Contains(t, err.Error(), "scope", "must hint about token scopes")
	assert.Contains(t, err.Error(), "melange auth status")
}

func TestRepoCreateInvalidModelTypeExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "create", "whisper-tiny", "--model-type", "vision")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests, "invalid flags must fail before any request")
}

func TestRepoCreateInvalidUseCaseExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "create", "whisper-tiny", "--use-case", "gaming")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestRepoCreateRejectsAccountPrefixExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "repo", "create", "zetic/whisper-tiny")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}
