// Package model_test exercises the model breadth commands (list, view,
// targets, set-default, import, download) through the full root command so
// persistent flags (--no-color, --no-input) and exit-code mapping apply.
package model_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

// testNow anchors relative timestamps; truncated to seconds so RFC3339
// round-trips byte-exact through fixtures and command output.
var testNow = time.Now().UTC().Truncate(time.Second)

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
	return runCtx(t, context.Background(), e, args...)
}

// runCtx runs the root command under a caller-owned context so tests can
// simulate SIGINT (context cancellation) mid-command.
func runCtx(t *testing.T, ctx context.Context, e *testEnv, args ...string) error {
	t.Helper()
	cmd := root.NewCmdRoot(e.f)
	cmd.SetIn(e.in)
	cmd.SetOut(e.out)
	cmd.SetErr(e.errOut)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}

func jsonStub(status int, body string) httpmock.Responder {
	return httpmock.WithHeader(httpmock.StatusStringResponse(status, body), "Content-Type", "application/json")
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

func ts(t time.Time) string { return t.Format(time.RFC3339) }

// modelJSON builds one ModelSummary object as the API would emit it.
func modelJSON(key string, version int, state string, isDefault bool, created time.Time) string {
	return fmt.Sprintf(
		`{"key":%q,"version":%d,"type":"onnx","state":%q,"is_default":%t,"created_at":%q,"updated_at":%q}`,
		key, version, state, isDefault, ts(created), ts(created))
}

func modelPage(count int, models ...string) string {
	out := `{"results":[`
	for i, m := range models {
		if i > 0 {
			out += ","
		}
		out += m
	}
	return out + fmt.Sprintf(`],"count":%d}`, count)
}

const (
	modelsPath = "/v1/repos/zetic/whisper/models"
	notFound   = `{"type":"error","error":{"type":"not_found_error","message":"repository zetic/whisper not found"},"request_id":"req_4"}`
	forbidden  = `{"type":"error","error":{"type":"permission_error","message":"token lacks access"},"request_id":"req_5"}`
	badAuth    = `{"type":"error","error":{"type":"authentication_error","message":"invalid token"},"request_id":"req_1"}`
)

// ---------------------------------------------------------------------------
// model list
// ---------------------------------------------------------------------------

func TestModelListTableTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(2,
		modelJSON("m_new", 3, "ready", true, testNow.Add(-2*time.Hour)),
		modelJSON("m_old", 1, "failed", false, testNow.Add(-72*time.Hour)))))

	require.NoError(t, run(t, e, "--no-color", "model", "list", "-R", "zetic/whisper"))

	want := "KEY    VERSION  TYPE  STATE   DEFAULT  CREATED\n" +
		"m_new  3        onnx  ready   ✓        2h ago\n" +
		"m_old  1        onnx  failed           3d ago\n"
	assert.Equal(t, want, e.out.String())

	require.Len(t, e.reg.Requests, 1)
	assert.Equal(t, "30", e.reg.Requests[0].URL.Query().Get("limit"), "default --limit is 30")
}

func TestModelListStateColoredOnTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	t.Setenv("CLICOLOR_FORCE", "1")
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(2,
		modelJSON("m_new", 3, "ready", true, testNow),
		modelJSON("m_old", 1, "failed", false, testNow))))

	require.NoError(t, run(t, e, "model", "list", "-R", "zetic/whisper"))
	assert.Contains(t, e.out.String(), "\x1b[32mready\x1b[0m", "ready is green")
	assert.Contains(t, e.out.String(), "\x1b[31mfailed\x1b[0m", "failed is red")
}

func TestModelListNonTTYTabSeparated(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(2,
		modelJSON("m_new", 3, "ready", true, testNow.Add(-2*time.Hour)),
		modelJSON("m_old", 1, "converting", false, testNow.Add(-72*time.Hour)))))

	require.NoError(t, run(t, e, "model", "list", "-R", "zetic/whisper"))

	want := fmt.Sprintf("m_new\t3\tonnx\tready\ttrue\t%s\n", ts(testNow.Add(-2*time.Hour))) +
		fmt.Sprintf("m_old\t1\tonnx\tconverting\tfalse\t%s\n", ts(testNow.Add(-72*time.Hour)))
	assert.Equal(t, want, e.out.String())
	assert.Empty(t, e.errOut.String())
}

func TestModelListJSONPassthrough(t *testing.T) {
	e := setup(t)
	body := modelPage(1, modelJSON("m_new", 3, "ready", true, testNow))
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "model", "list", "-R", "zetic/whisper", "--json"))
	assert.Equal(t, body+"\n", e.out.String(),
		"--json must emit the page envelope exactly as the API returned it")
}

func TestModelListEmptyTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(0)))

	require.NoError(t, run(t, e, "--no-color", "model", "list", "-R", "zetic/whisper"))
	assert.Empty(t, e.out.String(), "stdout stays clean")
	assert.Contains(t, e.errOut.String(), "No models found")
}

func TestModelListPaginateMergesPages(t *testing.T) {
	e := setup(t)
	m1 := modelJSON("m_1", 1, "ready", false, testNow)
	m2 := modelJSON("m_2", 2, "ready", true, testNow)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(2, m1)))
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(2, m2)))

	require.NoError(t, run(t, e, "model", "list", "-R", "zetic/whisper", "--paginate", "--json"))

	want := `{"count":2,"results":[` + m1 + `,` + m2 + `]}`
	assert.Equal(t, want+"\n", e.out.String())

	require.Len(t, e.reg.Requests, 2)
	assert.Equal(t, "100", e.reg.Requests[0].URL.Query().Get("limit"))
	assert.Equal(t, "1", e.reg.Requests[1].URL.Query().Get("offset"))
	e.reg.Verify(t)
}

func TestModelListPaginateWithLimitExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "model", "list", "-R", "zetic/whisper", "--paginate", "--limit", "5")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestModelListInvalidLimitExits2(t *testing.T) {
	for _, limit := range []string{"0", "101"} {
		t.Run(limit, func(t *testing.T) {
			e := setup(t)
			err := run(t, e, "model", "list", "-R", "zetic/whisper", "--limit", limit)
			require.Error(t, err)
			assert.Equal(t, 2, cmdutil.ExitCode(err))
			assert.Contains(t, err.Error(), "--limit must be between 1 and 100")
			assert.Empty(t, e.reg.Requests)
		})
	}
}

func TestModelListRequiresRepoExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "model", "list")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "-R ACCOUNT/REPO")
	assert.Empty(t, e.reg.Requests)
}

func TestModelListNotFoundExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(404, notFound))

	err := run(t, e, "model", "list", "-R", "zetic/whisper")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "repository zetic/whisper not found")
}

func TestModelListUnauthenticatedExits4(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(401, badAuth))

	err := run(t, e, "model", "list", "-R", "zetic/whisper")
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))
}

// ---------------------------------------------------------------------------
// model view
// ---------------------------------------------------------------------------

func detailJSON(state string, failureCode string) string {
	fc := "null"
	if failureCode != "" {
		fc = fmt.Sprintf("%q", failureCode)
	}
	terminal := state == "ready" || state == "failed"
	return fmt.Sprintf(`{"key":"m_ab12cd","version":3,"type":"onnx","state":%q,"is_default":true,`+
		`"terminal":%t,"download_ready":%t,"failure_code":%s,"source_type":"upload",`+
		`"created_at":%q,"updated_at":%q}`,
		state, terminal, state == "ready", fc, ts(testNow.Add(-2*time.Hour)), ts(testNow.Add(-time.Hour)))
}

const modelPath = "/v1/repos/zetic/whisper/models/m_ab12cd"

func TestModelViewTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", modelPath), jsonStub(200, detailJSON("ready", "")))

	require.NoError(t, run(t, e, "--no-color", "model", "view", "m_ab12cd", "-R", "zetic/whisper"))

	out := e.out.String()
	assert.Contains(t, out, "m_ab12cd in zetic/whisper")
	assert.Contains(t, out, "State:           ready")
	assert.Contains(t, out, "Version:         3")
	assert.Contains(t, out, "Default:         yes")
	assert.Contains(t, out, "Source:          upload")
	assert.Contains(t, out, "Download ready:  yes")
	assert.Contains(t, out, "melange deploy guide m_ab12cd -R zetic/whisper")
}

func TestModelViewNonTTYKeyValueLines(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", modelPath), jsonStub(200, detailJSON("failed", "conversion_error")))

	require.NoError(t, run(t, e, "model", "view", "m_ab12cd", "-R", "zetic/whisper"))

	want := "key\tm_ab12cd\n" +
		"version\t3\n" +
		"type\tonnx\n" +
		"state\tfailed\n" +
		"is_default\ttrue\n" +
		"source_type\tupload\n" +
		"terminal\ttrue\n" +
		"download_ready\tfalse\n" +
		"failure_code\tconversion_error\n" +
		fmt.Sprintf("created_at\t%s\n", ts(testNow.Add(-2*time.Hour))) +
		fmt.Sprintf("updated_at\t%s\n", ts(testNow.Add(-time.Hour)))
	assert.Equal(t, want, e.out.String())
}

func TestModelViewNonTTYEscapeEveryValue(t *testing.T) {
	e := setup(t)
	body := strings.Replace(detailJSON("failed", "conversion_error"),
		`"failure_code":"conversion_error"`, `"failure_code":"line1\nline2\tC:\\models"`, 1)
	e.reg.Register(httpmock.REST("GET", modelPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "model", "view", "m_ab12cd", "-R", "zetic/whisper"))
	assert.Contains(t, e.out.String(), "failure_code\t"+`line1\nline2\tC:\\models`+"\n")
	assert.NotContains(t, e.out.String(), "line1\nline2", "one value must remain one physical TSV row")
}

func TestModelViewJSON(t *testing.T) {
	e := setup(t)
	body := detailJSON("ready", "")
	e.reg.Register(httpmock.REST("GET", modelPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "model", "view", "m_ab12cd", "-R", "zetic/whisper", "--json"))
	assert.Equal(t, body+"\n", e.out.String())
}

func TestModelViewNotFoundExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", modelPath), jsonStub(404, notFound))

	err := run(t, e, "model", "view", "m_ab12cd", "-R", "zetic/whisper")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
}

func TestModelViewRequiresRepoExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "model", "view", "m_ab12cd")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

// ---------------------------------------------------------------------------
// model targets
// ---------------------------------------------------------------------------

const targetsPath = "/v1/repos/zetic/whisper/models/m_ab12cd/targets"

func targetsBody() string {
	return `{"results":[` +
		`{"target_id":"tm_71","kind":"general","target":"qnn","quant_type":"fp16",` +
		`"compatibility":{"ap_types":["npu"],"soc_manufacturer":"qualcomm","soc_model":"sm8650","os":"android"},` +
		`"download_size":52428800,"created_at":"` + ts(testNow.Add(-24*time.Hour)) + `"},` +
		`{"target_id":"ltm_9","kind":"llm","target":"llama-cpp","quant_type":"q4_k_m",` +
		`"compatibility":null,"download_size":1073741824,"created_at":"` + ts(testNow.Add(-48*time.Hour)) + `"}` +
		`],"count":2}`
}

func TestModelTargetsTableTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", targetsPath), jsonStub(200, targetsBody()))

	require.NoError(t, run(t, e, "--no-color", "model", "targets", "m_ab12cd", "-R", "zetic/whisper"))

	want := "TARGET_ID  KIND     TARGET     QUANT   COMPATIBILITY   SIZE\n" +
		"tm_71      general  qnn        fp16    sm8650/android  50.0 MiB\n" +
		"ltm_9      llm      llama-cpp  q4_k_m  -               1.0 GiB\n"
	assert.Equal(t, want, e.out.String())
}

func TestModelTargetsNonTTYRawBytes(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", targetsPath), jsonStub(200, targetsBody()))

	require.NoError(t, run(t, e, "model", "targets", "m_ab12cd", "-R", "zetic/whisper"))

	want := "tm_71\tgeneral\tqnn\tfp16\tsm8650/android\t52428800\n" +
		"ltm_9\tllm\tllama-cpp\tq4_k_m\t-\t1073741824\n"
	assert.Equal(t, want, e.out.String())
}

func TestModelTargetsJSONPassthrough(t *testing.T) {
	e := setup(t)
	body := targetsBody()
	e.reg.Register(httpmock.REST("GET", targetsPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "model", "targets", "m_ab12cd", "-R", "zetic/whisper", "--json"))
	assert.Equal(t, body+"\n", e.out.String())
}

func TestModelTargetsEmptyTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", targetsPath), jsonStub(200, `{"results":[],"count":0}`))

	require.NoError(t, run(t, e, "--no-color", "model", "targets", "m_ab12cd", "-R", "zetic/whisper"))
	assert.Empty(t, e.out.String())
	assert.Contains(t, e.errOut.String(), "No targets found")
}

func TestModelTargetsNotFoundExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", targetsPath), jsonStub(404, notFound))

	err := run(t, e, "model", "targets", "m_ab12cd", "-R", "zetic/whisper")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
}

func TestModelTargetsRequiresRepoExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "model", "targets", "m_ab12cd")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

// ---------------------------------------------------------------------------
// model set-default
// ---------------------------------------------------------------------------

const defaultPath = "/v1/repos/zetic/whisper/models/m_ab12cd/default"

func TestModelSetDefaultHappy(t *testing.T) {
	e := setup(t)
	body := modelJSON("m_ab12cd", 3, "ready", true, testNow)
	e.reg.Register(httpmock.REST("PUT", defaultPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "model", "set-default", "m_ab12cd", "-R", "zetic/whisper"))

	require.Len(t, e.reg.Requests, 1)
	assert.Contains(t, e.errOut.String(), "✓ Set m_ab12cd (version 3) as the default model for zetic/whisper")
	assert.Empty(t, e.out.String())
}

func TestModelSetDefaultJSON(t *testing.T) {
	e := setup(t)
	body := modelJSON("m_ab12cd", 3, "ready", true, testNow)
	e.reg.Register(httpmock.REST("PUT", defaultPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "model", "set-default", "m_ab12cd", "-R", "zetic/whisper", "--json"))
	assert.Equal(t, body+"\n", e.out.String())
}

func TestModelSetDefaultNotFoundExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("PUT", defaultPath), jsonStub(404, notFound))

	err := run(t, e, "model", "set-default", "m_ab12cd", "-R", "zetic/whisper")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
}

func TestModelSetDefaultForbiddenExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("PUT", defaultPath), jsonStub(403, forbidden))

	err := run(t, e, "model", "set-default", "m_ab12cd", "-R", "zetic/whisper")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "token lacks access")
}

func TestModelSetDefaultRequiresRepoExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "model", "set-default", "m_ab12cd")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

// ---------------------------------------------------------------------------
// model import
// ---------------------------------------------------------------------------

const importPath = "/v1/repos/zetic/whisper/models/import"

func TestModelImportHappy(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", importPath),
		jsonStub(201, `{"key":"m_hf1","version":1,"state":"converting"}`))

	require.NoError(t, run(t, e, "model", "import", "meta-llama/Llama-3.2-1B", "-R", "zetic/whisper"))

	require.Len(t, e.reg.Requests, 1)
	req := e.reg.Requests[0]
	assert.NotEmpty(t, req.Header.Get("Idempotency-Key"), "import must carry an Idempotency-Key")
	body := requestBody(t, req)
	assert.Equal(t, "meta-llama/Llama-3.2-1B", body["hf_repo"])
	assert.NotContains(t, body, "revision", "revision is reserved and must not be sent")

	assert.Contains(t, e.errOut.String(), "✓ Import started: model m_hf1 version 1 (state converting)")
	assert.Empty(t, e.out.String())
}

func TestModelImportJSON(t *testing.T) {
	e := setup(t)
	body := `{"key":"m_hf1","version":1,"state":"converting"}`
	e.reg.Register(httpmock.REST("POST", importPath), jsonStub(201, body))

	require.NoError(t, run(t, e, "model", "import", "meta-llama/Llama-3.2-1B", "-R", "zetic/whisper", "--json"))
	assert.Equal(t, body+"\n", e.out.String())
}

func TestModelImportReplayReturns200(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", importPath),
		jsonStub(200, `{"key":"m_hf1","version":1,"state":"converting"}`))

	require.NoError(t, run(t, e, "model", "import", "meta-llama/Llama-3.2-1B", "-R", "zetic/whisper"))
	assert.Contains(t, e.errOut.String(), "✓ Import started")
}

func TestModelImportNonLLMRepoSurfaces422(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", importPath),
		jsonStub(422, `{"type":"error","error":{"type":"invalid_request_error","message":"import requires a repository with model_type llm"},"request_id":"req_7"}`))

	err := run(t, e, "model", "import", "meta-llama/Llama-3.2-1B", "-R", "zetic/whisper")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "import requires a repository with model_type llm",
		"the server's llm-repo-only 422 must surface verbatim")
}

func TestModelImportRequiresRepoExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "model", "import", "meta-llama/Llama-3.2-1B")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestModelImportUnauthenticatedExits4(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", importPath), jsonStub(401, badAuth))

	err := run(t, e, "model", "import", "meta-llama/Llama-3.2-1B", "-R", "zetic/whisper")
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))
}
