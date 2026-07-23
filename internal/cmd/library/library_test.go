// Package library_test exercises `melange library` (list, view, providers)
// through the full root command so persistent flags and exit-code mapping
// apply.
package library_test

import (
	"bytes"
	"context"
	"fmt"
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

func ts(t time.Time) string { return t.Format(time.RFC3339) }

const (
	modelsPath    = "/v1/library/models"
	providersPath = "/v1/library/providers"
	forbidden     = `{"type":"error","error":{"type":"permission_error","message":"token lacks access"},"request_id":"req_2"}`
	notFound      = `{"type":"error","error":{"type":"not_found_error","message":"no such model"},"request_id":"req_1"}`
)

func modelItem(fullName, provider, useCase, typ string, created time.Time) string {
	return fmt.Sprintf(
		`{"account":"zetic","name":"whisper","full_name":%q,"model_type":%q,"provider":{"name":%q},"use_case":%q,"tags":["a"],"created_at":%q}`,
		fullName, typ, provider, useCase, ts(created))
}

func modelPage(count int, items ...string) string {
	return fmt.Sprintf(`{"count":%d,"results":[%s]}`, count, strings.Join(items, ","))
}

// ---------------------------------------------------------------------------
// library list
// ---------------------------------------------------------------------------

func TestLibraryListTableTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(1,
		modelItem("zetic/whisper-tiny", "Zetic", "speech", "onnx", testNow.Add(-2*time.Hour)))))

	require.NoError(t, run(t, e, "--no-color", "library", "list"))

	out := e.out.String()
	assert.Contains(t, out, "MODEL")
	assert.Contains(t, out, "zetic/whisper-tiny")
	assert.Contains(t, out, "Zetic")
	assert.Contains(t, out, "speech")
	assert.Contains(t, out, "2h ago")
}

func TestLibraryListFiltersAsQueryParams(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(0)))

	require.NoError(t, run(t, e, "library", "list",
		"--task", "vision", "--task", "llm", "--search", "whisper", "--provider", "Zetic"))

	require.Len(t, e.reg.Requests, 1)
	q := e.reg.Requests[0].URL.Query()
	assert.Equal(t, []string{"vision", "llm"}, q["task"], "repeatable task filter")
	assert.Equal(t, "whisper", q.Get("search"))
	assert.Equal(t, "Zetic", q.Get("provider"))
	assert.Equal(t, "30", q.Get("limit"))
}

func TestLibraryListInvalidTaskExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "library", "list", "--task", "bogus")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestLibraryListHelpDocumentsSeparatorInsensitiveSearch(t *testing.T) {
	e := setup(t)
	require.NoError(t, run(t, e, "library", "list", "--help"))

	help := e.out.String()
	assert.Contains(t, help, "Case- and separator-insensitive")
	assert.Contains(t, help, "name or full_name")
}

func TestLibraryListInvalidLimitExits2(t *testing.T) {
	for _, limit := range []string{"0", "101"} {
		t.Run(limit, func(t *testing.T) {
			e := setup(t)
			err := run(t, e, "library", "list", "--limit", limit)
			require.Error(t, err)
			assert.Equal(t, 2, cmdutil.ExitCode(err))
			assert.Contains(t, err.Error(), "--limit must be between 1 and 100")
			assert.Empty(t, e.reg.Requests)
		})
	}
}

func TestLibraryListNonTTYTabSeparated(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(1,
		modelItem("zetic/whisper-tiny", "Zetic", "speech", "onnx", testNow))))

	require.NoError(t, run(t, e, "library", "list"))
	want := fmt.Sprintf("zetic/whisper-tiny\tZetic\tspeech\tonnx\t%s\n", ts(testNow))
	assert.Equal(t, want, e.out.String())
}

func TestLibraryListJSONByteExact(t *testing.T) {
	e := setup(t)
	body := modelPage(1, modelItem("zetic/whisper-tiny", "Zetic", "speech", "onnx", testNow))
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "library", "list", "--json"))
	assert.Equal(t, body+"\n", e.out.String())
}

func TestLibraryListEmptyTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(0)))

	require.NoError(t, run(t, e, "--no-color", "library", "list"))
	assert.Empty(t, e.out.String())
	assert.Contains(t, e.errOut.String(), "No models found")
}

func TestLibraryListPaginateMerges(t *testing.T) {
	e := setup(t)
	m1 := modelItem("zetic/a", "Zetic", "vision", "onnx", testNow)
	m2 := modelItem("zetic/b", "Zetic", "vision", "onnx", testNow)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(2, m1)))
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(200, modelPage(2, m2)))

	require.NoError(t, run(t, e, "library", "list", "--paginate", "--json"))
	want := `{"count":2,"results":[` + m1 + `,` + m2 + `]}`
	assert.Equal(t, want+"\n", e.out.String())
	require.Len(t, e.reg.Requests, 2)
	assert.Equal(t, "100", e.reg.Requests[0].URL.Query().Get("limit"))
	assert.Equal(t, "1", e.reg.Requests[1].URL.Query().Get("offset"))
}

func TestLibraryListForbiddenExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", modelsPath), jsonStub(403, forbidden))
	err := run(t, e, "library", "list")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "token lacks access")
}

// ---------------------------------------------------------------------------
// library view
// ---------------------------------------------------------------------------

func libModelDetail(readme string) string {
	return fmt.Sprintf(
		`{"account":"zetic","name":"whisper-tiny","full_name":"zetic/whisper-tiny","model_type":"onnx","provider":{"name":"Zetic"},"use_case":"speech","tags":["asr","tiny"],"description":"A tiny model","readme":%q,"created_at":%q}`,
		readme, ts(testNow))
}

func TestLibraryViewReadmeExcerpt(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	// 15-line readme → excerpt is the first 10 lines plus a truncation note.
	var lines []string
	for i := 1; i <= 15; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	e.reg.Register(httpmock.REST("GET", "/v1/library/models/zetic/whisper-tiny"),
		jsonStub(200, libModelDetail(strings.Join(lines, "\n"))))

	require.NoError(t, run(t, e, "--no-color", "library", "view", "zetic/whisper-tiny"))

	out := e.out.String()
	assert.Contains(t, out, "zetic/whisper-tiny")
	assert.Contains(t, out, "Provider:  Zetic")
	assert.Contains(t, out, "line1")
	assert.Contains(t, out, "line10")
	assert.NotContains(t, out, "line11", "readme is excerpted to 10 lines")
	assert.Contains(t, out, "readme truncated")
	assert.Contains(t, out, "melange model list -R zetic/whisper-tiny")
	assert.Contains(t, out, "melange deploy guide MODEL_KEY -R zetic/whisper-tiny")
}

func TestLibraryViewShortReadmeNoTruncationNote(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", "/v1/library/models/zetic/whisper-tiny"),
		jsonStub(200, libModelDetail("only one line")))

	require.NoError(t, run(t, e, "--no-color", "library", "view", "zetic/whisper-tiny"))
	out := e.out.String()
	assert.Contains(t, out, "only one line")
	assert.NotContains(t, out, "readme truncated")
}

func TestLibraryViewHumanOutputNeutralizesOSC52(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	body := strings.Replace(
		libModelDetail("safe placeholder text"),
		"safe placeholder text",
		`safe\u001b]52;c;Y2xpcGJvYXJkLXNlY3JldA==\u0007 text`,
		1,
	)
	e.reg.Register(httpmock.REST("GET", "/v1/library/models/zetic/whisper-tiny"),
		jsonStub(200, body))

	require.NoError(t, run(t, e, "--no-color", "library", "view", "zetic/whisper-tiny"))

	assert.Contains(t, e.out.String(), "safe text")
	assert.NotContains(t, e.out.String(), "\x1b")
	assert.NotContains(t, e.out.String(), "Y2xpcGJvYXJk")
}

func TestLibraryViewNonTTYOmitsReadme(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/library/models/zetic/whisper-tiny"),
		jsonStub(200, libModelDetail("secret readme")))

	require.NoError(t, run(t, e, "library", "view", "zetic/whisper-tiny"))
	out := e.out.String()
	assert.Contains(t, out, "full_name\tzetic/whisper-tiny")
	assert.Contains(t, out, "tags\tasr,tiny")
	assert.NotContains(t, out, "secret readme", "TSV omits the readme")
}

func TestLibraryViewNonTTYEscapesDescriptionControlCharacters(t *testing.T) {
	e := setup(t)
	body := strings.Replace(
		libModelDetail("secret readme"),
		`"description":"A tiny model"`,
		`"description":"first line\nsecond\tcell\\tail\r"`,
		1,
	)
	e.reg.Register(httpmock.REST("GET", "/v1/library/models/zetic/whisper-tiny"),
		jsonStub(200, body))

	require.NoError(t, run(t, e, "library", "view", "zetic/whisper-tiny"))

	assert.Contains(t, e.out.String(), "description\tfirst line\\nsecond\\tcell\\\\tail\\r\n")
	assert.Len(t, strings.Split(strings.TrimSuffix(e.out.String(), "\n"), "\n"), 9,
		"each key/value field must occupy exactly one physical line")
}

func TestLibraryViewJSONByteExact(t *testing.T) {
	e := setup(t)
	body := libModelDetail("full readme text")
	e.reg.Register(httpmock.REST("GET", "/v1/library/models/zetic/whisper-tiny"), jsonStub(200, body))

	require.NoError(t, run(t, e, "library", "view", "zetic/whisper-tiny", "--json"))
	assert.Equal(t, body+"\n", e.out.String())
}

func TestLibraryViewNotFoundExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/library/models/zetic/nope"), jsonStub(404, notFound))
	err := run(t, e, "library", "view", "zetic/nope")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
}

func TestLibraryViewBadArgExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "library", "view", "no-slash")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestLibraryViewHelpBridgesDiscoveryToReportsWithoutConflatingIDs(t *testing.T) {
	e := setup(t)
	require.NoError(t, run(t, e, "library", "view", "--help"))

	help := e.out.String()
	assert.Contains(t, help, "Library entries are repository coordinates, not converted model keys")
	assert.Contains(t, help, `repo=$(melange library list --search QUERY --jq '.results[0].full_name')`)
	assert.Contains(t, help, `key=$(melange model list -R "$repo" --jq '.results | (map(select(.is_default and .state=="ready")) + map(select(.state=="ready")))[0].key // empty')`)
	assert.Contains(t, help, `[ -n "$key" ] ||`)
	assert.Contains(t, help, `melange report view "$key" -R "$repo" --json`)
	assert.Contains(t, help, `melange deploy guide "$key" -R "$repo" --language android-kotlin --mode auto`)
	assert.NotContains(t, help, `melange report view "$key" -R "$repo" --type llm`)
	assert.Contains(t, help, "Never import a library model solely to read its public benchmarks")
}

// ---------------------------------------------------------------------------
// library providers
// ---------------------------------------------------------------------------

func TestLibraryProvidersTable(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	body := `{"count":2,"results":[{"name":"Zetic","model_count":12},{"name":"Acme","model_count":3}]}`
	e.reg.Register(httpmock.REST("GET", providersPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "--no-color", "library", "providers"))
	out := e.out.String()
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "MODELS")
	assert.Contains(t, out, "Zetic")
	assert.Contains(t, out, "12")
}

func TestLibraryProvidersNonTTY(t *testing.T) {
	e := setup(t)
	body := `{"count":1,"results":[{"name":"Zetic","model_count":12}]}`
	e.reg.Register(httpmock.REST("GET", providersPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "library", "providers"))
	assert.Equal(t, "Zetic\t12\n", e.out.String())
}

func TestLibraryProvidersJSONByteExact(t *testing.T) {
	e := setup(t)
	body := `{"count":1,"results":[{"name":"Zetic","model_count":12}]}`
	e.reg.Register(httpmock.REST("GET", providersPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "library", "providers", "--json"))
	assert.Equal(t, body+"\n", e.out.String())
}
