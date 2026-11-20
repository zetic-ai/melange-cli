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
	`{"key":"per_clip_ms","group":"speed","category":"speed","label":"Per clip","unit":"ms","better":"lower","keyed_by":null,"summary":true,"approximate":false,"maximum":null},` +
	`{"key":"encoder_ms","group":"speed","category":"speed","label":"Encoder","unit":"ms","better":"lower","keyed_by":null,"summary":false,"approximate":false,"maximum":null},` +
	`{"key":"wer_pct","group":"accuracy","category":"accuracy","label":"WER","unit":"%","better":"lower","keyed_by":"test_set","summary":true,"approximate":false,"maximum":100},` +
	`{"key":"memory_mb","group":"speed","category":"memory","label":"Memory","unit":"MB","better":"lower","keyed_by":null,"summary":true,"approximate":false,"maximum":null}],` +
	`"llm":[` +
	`{"key":"decode_tps","group":"speed","category":"speed","label":"Decode","unit":"tokens/s","better":"higher","keyed_by":null,"summary":true,"approximate":false,"maximum":null},` +
	`{"key":"ttft_ms","group":"speed","category":"speed","label":"Time to first token","unit":"ms","better":"lower","keyed_by":"prompt_tokens","summary":true,"approximate":false,"maximum":null},` +
	`{"key":"energy_per_token_mj","group":"speed","category":"energy","label":"Energy per token","unit":"mJ","better":"lower","keyed_by":null,"summary":true,"approximate":true,"maximum":null}]},` +
	`"targets":[` +
	`{"soc":"SOC-A","name":"Chip A","npu":"NPU 1","form_factor":"wearable","has_results":true,"device":{"name":"Board A","model":null},"notes":[]},` +
	`{"soc":"SOC-B","name":"Chip B","npu":"NPU 2","form_factor":"phone","has_results":false,"device":{"name":"Phone B","model":"PB-1"},"notes":[]}],` +
	`"models":[` +
	`{"slug":"speech","task":"asr","name":"Speech","vendor":"Vendor & Co","order":10,"description":null,` +
	`"variants":[{"key":"tiny","label":"tiny"},{"key":"base","label":"base"}],` +
	`"results":[{"soc":"SOC-A","measured_on":"Board A","as_of":"2026-11-18","revision":2,"updated_at":"2026-11-18T09:00:00Z",` +
	`"software":{"SDK":"1.0"},"method":{"speed":"m","accuracy":"m"},"highlights":["Speed uses 20 % less memory."],` +
	`"notes":{"speed":[],"accuracy":[]},"tables":[],"builds":[` +
	`{"key":"tiny-fast","label":"W4","runtime":"llama_cpp","variant":"tiny","status":"measured","status_note":null,"modes":["speed"],"highlight":true,"baseline":false,"notes":[],` +
	`"speed":{"encoder_ms":23.46,"per_clip_ms":93.7,"memory_mb":121.0},"accuracy":{"wer_pct":{"test-other":14.16,"test-clean":5.74}}},` +
	`{"key":"tiny-exact","label":"W8","runtime":"llama_cpp","variant":"tiny","status":"measured","status_note":null,"modes":["accuracy"],"highlight":true,"baseline":false,"notes":[],` +
	`"speed":{"per_clip_ms":105.4,"memory_mb":150.0},"accuracy":{"wer_pct":{"test-other":13.6,"test-clean":6.1}}},` +
	`{"key":"tiny-float","label":"Float (reference)","runtime":"float_reference","variant":"tiny","status":"measured","status_note":null,"modes":[],"highlight":false,"baseline":false,"notes":[],` +
	`"speed":{},"accuracy":{"wer_pct":{"test-other":13.5,"test-clean":6.0}}},` +
	`{"key":"base-cpu","label":"CPU","runtime":"llama_cpp","variant":"base","status":"unsupported","status_note":"graph too large","modes":[],"highlight":false,"baseline":false,"notes":[],` +
	`"speed":{},"accuracy":{}}]}]},` +
	`{"slug":"chat","task":"llm","name":"Chat","vendor":null,"order":20,"description":null,"variants":[],` +
	`"results":[{"soc":"SOC-A","measured_on":"Board A","as_of":"2026-11-17","revision":1,"updated_at":"2026-11-17T09:00:00Z",` +
	`"software":{},"method":{},"highlights":[],"notes":{"speed":[],"accuracy":[]},"tables":[],"builds":[` +
	`{"key":"ours","label":"Q4","runtime":"llama_cpp","variant":null,"status":"measured","status_note":null,"modes":["speed","accuracy"],"highlight":true,"baseline":false,"notes":[],` +
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
		"Auto runs the Speed build.",
		"test-other: 14.16, test-clean: 5.74",
		"- Speed uses 20 % less memory.",
	} {
		assert.Contains(t, speech, want)
	}
	// Columns: variant, mode, build, status, then the summary metrics by
	// category — speed, memory, accuracy — whatever the vocabulary order.
	assert.Regexp(t, `VARIANT +MODE +BUILD +STATUS +PER CLIP \(MS\) +MEMORY \(MB\) +WER \(%, BY TEST SET\)`, speech)
	assert.NotContains(t, speech, "ENCODER", "non-summary metrics stay out of the table")
	// Per variant: Auto and Speed show the speed build, Accuracy its own;
	// builds serving no mode follow with mode "-"; a mode nobody serves
	// (base has none) is left out.
	assert.Regexp(t, `(?s)tiny +Auto +W4 +measured +93\.7 +121\.0 .*`+
		`tiny +Speed +W4 +measured +93\.7 .*`+
		`tiny +Accuracy +W8 +measured +105\.4 +150\.0 .*`+
		`tiny +- +Float \(reference\) +measured +- +- +test-other: 13\.5.*`+
		`base +- +CPU +unsupported: graph too large`, speech)
	assert.NotRegexp(t, `base +(Auto|Speed|Accuracy)`, speech)
	assert.NotContains(t, speech, "baseline")
	assert.NotContains(t, speech, "* ")

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
	// One build serving both modes fills all three rows.
	assert.Regexp(t, `(?s)Auto +Q4 .*Speed +Q4 .*Accuracy +Q4 `, chat)
	assert.NotContains(t, chat, "VARIANT", "no variant column for a model without variants")
	assert.NotContains(t, chat, "STATUS", "no status column when every build is measured")
}

func TestBenchmarksTSVIsOneMetricValuePerLine(t *testing.T) {
	e := setup(t)
	stubSheets(t, e)

	require.NoError(t, run(t, e, "program", "benchmarks", program))

	lines := strings.Split(strings.TrimRight(e.out.String(), "\n"), "\n")
	assert.Equal(t, []string{
		// Vocabulary order (every metric, not only the summary ones), keys in
		// API order except prompt lengths, which sort numerically; modes
		// comma-joined right after status.
		"SOC-A\tspeech\tasr\ttiny\ttiny-fast\tllama_cpp\tmeasured\tspeed\tper_clip_ms\t\t93.7\tms",
		"SOC-A\tspeech\tasr\ttiny\ttiny-fast\tllama_cpp\tmeasured\tspeed\tencoder_ms\t\t23.46\tms",
		"SOC-A\tspeech\tasr\ttiny\ttiny-fast\tllama_cpp\tmeasured\tspeed\twer_pct\ttest-other\t14.16\t%",
		"SOC-A\tspeech\tasr\ttiny\ttiny-fast\tllama_cpp\tmeasured\tspeed\twer_pct\ttest-clean\t5.74\t%",
		"SOC-A\tspeech\tasr\ttiny\ttiny-fast\tllama_cpp\tmeasured\tspeed\tmemory_mb\t\t121.0\tMB",
		"SOC-A\tspeech\tasr\ttiny\ttiny-exact\tllama_cpp\tmeasured\taccuracy\tper_clip_ms\t\t105.4\tms",
		"SOC-A\tspeech\tasr\ttiny\ttiny-exact\tllama_cpp\tmeasured\taccuracy\twer_pct\ttest-other\t13.6\t%",
		"SOC-A\tspeech\tasr\ttiny\ttiny-exact\tllama_cpp\tmeasured\taccuracy\twer_pct\ttest-clean\t6.1\t%",
		"SOC-A\tspeech\tasr\ttiny\ttiny-exact\tllama_cpp\tmeasured\taccuracy\tmemory_mb\t\t150.0\tMB",
		"SOC-A\tspeech\tasr\ttiny\ttiny-float\tfloat_reference\tmeasured\t\twer_pct\ttest-other\t13.5\t%",
		"SOC-A\tspeech\tasr\ttiny\ttiny-float\tfloat_reference\tmeasured\t\twer_pct\ttest-clean\t6.0\t%",
		// A build without metrics still says what its status is.
		"SOC-A\tspeech\tasr\tbase\tbase-cpu\tllama_cpp\tunsupported\t\t\t\t\t",
		"SOC-A\tchat\tllm\t\tours\tllama_cpp\tmeasured\tspeed,accuracy\tdecode_tps\t\t0.463\ttokens/s",
		"SOC-A\tchat\tllm\t\tours\tllama_cpp\tmeasured\tspeed,accuracy\tttft_ms\t128\t48.2\tms",
		"SOC-A\tchat\tllm\t\tours\tllama_cpp\tmeasured\tspeed,accuracy\tttft_ms\t1024\t194.5\tms",
		"SOC-A\tchat\tllm\t\tours\tllama_cpp\tmeasured\tspeed,accuracy\tenergy_per_token_mj\t\t3.452\tmJ",
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
	assert.Contains(t, e.out.String(), "SOC-A\tchat\tllm\t\tours\tllama_cpp\tmeasured\tspeed,accuracy\tdecode_tps\t\t0.463\ttokens/s")
}
