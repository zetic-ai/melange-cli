// Package program_test exercises `melange program benchmarks` through the
// full root command, against the shared contract fixture.
package program_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/edition"
	"github.com/zetic-ai/melange-cli/internal/fixturetest"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

type testEnv struct {
	f      *cmdutil.Factory
	reg    *httpmock.Registry
	out    *bytes.Buffer
	errOut *bytes.Buffer
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	t.Setenv("MELANGE_DEBUG", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("NO_COLOR", "")

	ios, _, out, errOut := iostreams.Test()
	reg := &httpmock.Registry{}
	f := &cmdutil.Factory{IOStreams: ios, Version: "test", HTTPTransport: reg}
	f.ApiClient = func() (*api.Client, error) {
		return cmdutil.NewAPIClient(f, "https://api.zetic.ai", "ztp_test")
	}
	return &testEnv{f: f, reg: reg, out: out, errOut: errOut}
}

func run(t *testing.T, e *testEnv, args ...string) error {
	t.Helper()
	cmd := root.NewCmdRoot(e.f)
	cmd.SetOut(e.out)
	cmd.SetErr(e.errOut)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func jsonStub(status int, body string) httpmock.Responder {
	return httpmock.WithHeader(httpmock.StatusStringResponse(status, body), "Content-Type", "application/json")
}

const (
	program = "ACME-PROGRAM"
	path    = "/v1/programs/ACME-PROGRAM/benchmarks"
)

// sheets is a synthetic program page. Keys are not in alphabetical order, and
// some values carry number text a float32 round trip would rewrite (0.463,
// 3.452), so a re-encode through typed structs shows up in the assertions.
const sheets = `{"schema_version":"program-benchmark/v1","program":"ACME-PROGRAM",` +
	`"metrics":{` +
	`"asr":[` +
	`{"key":"per_clip_ms","group":"speed","label":"Per clip","unit":"ms","better":"lower","keyed_by":null,"summary":true,"approximate":false,"maximum":null},` +
	`{"key":"encoder_ms","group":"speed","label":"Encoder","unit":"ms","better":"lower","keyed_by":null,"summary":false,"approximate":false,"maximum":null},` +
	`{"key":"wer_pct","group":"accuracy","label":"WER","unit":"%","better":"lower","keyed_by":"test_set","summary":true,"approximate":false,"maximum":100}],` +
	`"llm":[` +
	`{"key":"decode_tps","group":"speed","label":"Decode","unit":"tokens/s","better":"higher","keyed_by":null,"summary":true,"approximate":false,"maximum":null},` +
	`{"key":"ttft_ms","group":"speed","label":"Time to first token","unit":"ms","better":"lower","keyed_by":"prompt_tokens","summary":true,"approximate":false,"maximum":null},` +
	`{"key":"energy_per_token_mj","group":"speed","label":"Energy per token","unit":"mJ","better":"lower","keyed_by":null,"summary":true,"approximate":true,"maximum":null}]},` +
	`"targets":[` +
	`{"soc":"SOC-A","name":"Chip A","npu":"NPU 1","form_factor":"wearable","has_results":true,"device":{"name":"Board A","model":null},"notes":[]},` +
	`{"soc":"SOC-B","name":"Chip B","npu":"NPU 2","form_factor":"phone","has_results":false,"device":{"name":"Phone B","model":"PB-1"},"notes":[]}],` +
	`"models":[` +
	`{"slug":"speech","task":"asr","name":"Speech","vendor":"Vendor & Co","order":10,"description":null,` +
	`"variants":[{"key":"tiny","label":"tiny"},{"key":"base","label":"base"}],` +
	`"results":[{"soc":"SOC-A","measured_on":"Board A","as_of":"2026-11-18","revision":2,"updated_at":"2026-11-18T09:00:00Z",` +
	`"software":{"SDK":"1.0"},"method":{"speed":"m","accuracy":"m"},"highlights":["Ours is 1.1x faster."],` +
	`"notes":{"speed":[],"accuracy":[]},"tables":[],"builds":[` +
	`{"key":"tiny-ours","label":"Ours","runtime":"llama_cpp","variant":"tiny","status":"measured","status_note":null,"highlight":true,"baseline":false,"notes":[],` +
	`"speed":{"encoder_ms":23.46,"per_clip_ms":93.7},"accuracy":{"wer_pct":{"test-other":14.16,"test-clean":5.74}}},` +
	`{"key":"tiny-vendor","label":"Vendor","runtime":"qualcomm_qnn","variant":"tiny","status":"measured","status_note":null,"highlight":true,"baseline":true,"notes":[],` +
	`"speed":{"per_clip_ms":105.4},"accuracy":{"wer_pct":{"test-other":13.6,"test-clean":6.1}}},` +
	`{"key":"base-vendor","label":"Vendor","runtime":"qualcomm_qnn","variant":"base","status":"unsupported","status_note":"graph too large","highlight":false,"baseline":false,"notes":[],` +
	`"speed":{},"accuracy":{}}]}]},` +
	`{"slug":"chat","task":"llm","name":"Chat","vendor":null,"order":20,"description":null,"variants":[],` +
	`"results":[{"soc":"SOC-A","measured_on":"Board A","as_of":"2026-11-17","revision":1,"updated_at":"2026-11-17T09:00:00Z",` +
	`"software":{},"method":{},"highlights":[],"notes":{"speed":[],"accuracy":[]},"tables":[],"builds":[` +
	`{"key":"ours","label":"Ours","runtime":"llama_cpp","variant":null,"status":"measured","status_note":null,"highlight":true,"baseline":false,"notes":[],` +
	`"speed":{"decode_tps":0.463,"ttft_ms":{"1024":194.5,"128":48.2},"energy_per_token_mj":3.452},"accuracy":{}}]}]}],` +
	`"updated_at":"2026-11-18T09:00:00Z"}`

func stubSheets(t *testing.T, e *testEnv) string {
	t.Helper()
	e.reg.Register(httpmock.REST("GET", path), jsonStub(200, sheets))
	return sheets
}

// TestFixtureRendersInEveryLayout runs the shared contract fixture through
// all three layouts, so a backend payload change that breaks decoding fails
// here and not only in a user's terminal.
func TestFixtureRendersInEveryLayout(t *testing.T) {
	fx := fixturetest.Load(t, "get_program_benchmarks")
	body := string(fixturetest.Concretize(fx.Response.Body))
	for _, tty := range []bool{true, false} {
		e := setup(t)
		e.f.IOStreams.SetStdoutTTY(tty)
		e.reg.Register(httpmock.REST("GET", fx.Request.Path), jsonStub(200, body))
		program := fixturetest.SegmentAfter(fx.Request.Path, "programs")
		require.NoError(t, run(t, e, "program", "benchmarks", program))
		assert.NotEmpty(t, e.out.String())
	}
}

func TestBenchmarksJSONIsTheResponseVerbatim(t *testing.T) {
	e := setup(t)
	body := stubSheets(t, e)

	require.NoError(t, run(t, e, "program", "benchmarks", program, "--json"))
	assert.Equal(t, strings.TrimRight(body, " \t\r\n")+"\n", e.out.String())
}

func TestBenchmarksFiltersKeepOrderAndNumberText(t *testing.T) {
	e := setup(t)
	stubSheets(t, e)

	require.NoError(t, run(t, e, "program", "benchmarks", program,
		"--model", "CHAT", "--soc", "soc-a", "--json"))

	out := e.out.String()
	var page struct {
		Targets []struct {
			Soc string `json:"soc"`
		} `json:"targets"`
		Models []struct {
			Slug    string `json:"slug"`
			Results []struct {
				Soc string `json:"soc"`
			} `json:"results"`
		} `json:"models"`
		Metrics map[string]json.RawMessage `json:"metrics"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &page))
	require.Len(t, page.Targets, 1)
	assert.Equal(t, "SOC-A", page.Targets[0].Soc)
	require.Len(t, page.Models, 1)
	assert.Equal(t, "chat", page.Models[0].Slug)
	require.Len(t, page.Models[0].Results, 1)
	assert.Len(t, page.Metrics, 2, "the metric vocabulary passes through")

	assert.Contains(t, out, `"decode_tps":0.463`)
	assert.Contains(t, out, `"energy_per_token_mj":3.452`)
	assert.Contains(t, out, `"vendor":null,"order":20`, "member order and bytes survive")
	assert.Less(t, strings.Index(out, `"metrics"`), strings.Index(out, `"targets"`))
}

func TestBenchmarksSocWithoutResultsKeepsTheTargetOnly(t *testing.T) {
	e := setup(t)
	stubSheets(t, e)

	require.NoError(t, run(t, e, "program", "benchmarks", program, "--soc", "SOC-B", "--json",
		"--jq", "[(.targets | length), (.models | length), .targets[0].has_results]"))
	assert.Equal(t, "[1,0,false]\n", e.out.String())
}

func TestBenchmarksUnknownFilterNamesTheCatalog(t *testing.T) {
	for _, tc := range []struct {
		flag, value, want string
	}{
		{"--soc", "SOC-Z", "SoC \"SOC-Z\" is not in the ACME-PROGRAM catalog; it lists SOC-A, SOC-B"},
		{"--model", "vision", "model \"vision\" is not in the ACME-PROGRAM sheets; they list speech, chat"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			e := setup(t)
			stubSheets(t, e)
			err := run(t, e, "program", "benchmarks", program, tc.flag, tc.value)
			require.Error(t, err)
			assert.Equal(t, 1, cmdutil.ExitCode(err))
			assert.Equal(t, tc.want, err.Error())
			assert.Empty(t, e.out.String())
		})
	}
}

func TestBenchmarksTTY(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.f.IOStreams.SetTerminalWidth(400)
	stubSheets(t, e)

	require.NoError(t, run(t, e, "--no-color", "program", "benchmarks", program))

	out := e.out.String()
	speech := out[:strings.Index(out, "\nChat · LLM")]
	for _, want := range []string{
		"ACME-PROGRAM benchmarks, updated 2026-11-18",
		"Speech · ASR (speech to text) · Vendor & Co",
		"SOC-A Chip A · measured on Board A · as of 2026-11-18",
		// Variant, status and summary-metric columns, with units and keys.
		"VARIANT",
		"STATUS",
		"PER CLIP (MS)",
		"WER (%, BY TEST SET)",
		"* Vendor (baseline)",
		"unsupported: graph too large",
		"test-other: 14.16, test-clean: 5.74",
		// One build is outside the page summary, so highlights are marked.
		"* Ours",
		"* in the program page's summary",
		"- Ours is 1.1x faster.",
	} {
		assert.Contains(t, speech, want)
	}
	assert.NotContains(t, speech, "ENCODER", "non-summary metrics stay out of the table")

	chat := out[strings.Index(out, "\nChat · LLM"):]
	for _, want := range []string{
		"Chat · LLM\n",
		"TIME TO FIRST TOKEN (MS, BY PROMPT TOKENS)",
		"128: 48.2, 1024: 194.5",
		"~3.452",
		"No results yet:\n  SOC-B (Chip B, NPU 2), measured on Phone B PB-1",
		"`melange program benchmarks ACME-PROGRAM --json`",
	} {
		assert.Contains(t, chat, want)
	}
	assert.NotContains(t, chat, "VARIANT", "no variant column for a model without variants")
	assert.NotContains(t, chat, "STATUS", "no status column when every build is measured")
	assert.NotContains(t, chat, "* ", "no marks when every build is in the summary")
}

func TestBenchmarksTSVIsOneMetricValuePerLine(t *testing.T) {
	e := setup(t)
	stubSheets(t, e)

	require.NoError(t, run(t, e, "program", "benchmarks", program))

	lines := strings.Split(strings.TrimRight(e.out.String(), "\n"), "\n")
	assert.Equal(t, []string{
		// Vocabulary order (every metric, not only the summary ones), keys in
		// API order except prompt lengths, which sort numerically.
		"SOC-A\tspeech\tasr\ttiny\ttiny-ours\tllama_cpp\tmeasured\tper_clip_ms\t\t93.7\tms",
		"SOC-A\tspeech\tasr\ttiny\ttiny-ours\tllama_cpp\tmeasured\tencoder_ms\t\t23.46\tms",
		"SOC-A\tspeech\tasr\ttiny\ttiny-ours\tllama_cpp\tmeasured\twer_pct\ttest-other\t14.16\t%",
		"SOC-A\tspeech\tasr\ttiny\ttiny-ours\tllama_cpp\tmeasured\twer_pct\ttest-clean\t5.74\t%",
		"SOC-A\tspeech\tasr\ttiny\ttiny-vendor\tqualcomm_qnn\tmeasured\tper_clip_ms\t\t105.4\tms",
		"SOC-A\tspeech\tasr\ttiny\ttiny-vendor\tqualcomm_qnn\tmeasured\twer_pct\ttest-other\t13.6\t%",
		"SOC-A\tspeech\tasr\ttiny\ttiny-vendor\tqualcomm_qnn\tmeasured\twer_pct\ttest-clean\t6.1\t%",
		// A build without metrics still says what its status is.
		"SOC-A\tspeech\tasr\tbase\tbase-vendor\tqualcomm_qnn\tunsupported\t\t\t\t",
		"SOC-A\tchat\tllm\t\tours\tllama_cpp\tmeasured\tdecode_tps\t\t0.463\ttokens/s",
		"SOC-A\tchat\tllm\t\tours\tllama_cpp\tmeasured\tttft_ms\t128\t48.2\tms",
		"SOC-A\tchat\tllm\t\tours\tllama_cpp\tmeasured\tttft_ms\t1024\t194.5\tms",
		"SOC-A\tchat\tllm\t\tours\tllama_cpp\tmeasured\tenergy_per_token_mj\t\t3.452\tmJ",
	}, lines)
}

func TestBenchmarksNotFound(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/programs/NOPE/benchmarks"), jsonStub(404,
		`{"type":"error","error":{"type":"not_found_error","message":"Program not found"},"request_id":"req_1"}`))

	err := run(t, e, "program", "benchmarks", "NOPE")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "Program not found")
}

func TestBenchmarksRequiresProgram(t *testing.T) {
	e := setup(t)
	err := run(t, e, "program", "benchmarks")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestQualcommEditionRendersTheSameSheets(t *testing.T) {
	e := setup(t)
	e.f.Edition = edition.Qualcomm()
	stubSheets(t, e)

	require.NoError(t, run(t, e, "program", "benchmarks", program, "--model", "chat"))
	assert.Contains(t, e.out.String(), "SOC-A\tchat\tllm\t\tours\tllama_cpp\tmeasured\tdecode_tps\t\t0.463\ttokens/s")
}
