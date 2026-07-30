// Package report_test exercises `melange report view` through the full root
// command so persistent flags and exit-code mapping apply. The fixtures are
// built inline so the mode-selection assertions are legible against the
// derivation rule they encode.
package report_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

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

const (
	generalPath = "/v1/repos/zetic/whisper/models/m_x/reports/general"
	llmPath     = "/v1/repos/zetic/whisper/models/m_x/reports/llm"
	packagePath = "/v1/repos/zetic/whisper/models/m_x/reports/package"
	notFound    = `{"type":"error","error":{"type":"not_found_error","message":"no such report"},"request_id":"req_1"}`
	forbidden   = `{"type":"error","error":{"type":"permission_error","message":"token lacks access"},"request_id":"req_2"}`
)

// grec builds one general record.
func grec(device, ap, target, prec string, run int, metric string, value float32, unit string) string {
	return fmt.Sprintf(
		`{"device":{"marketing_name":%q,"name":%q},"ap_type":%q,"target":%q,"precision":%q,"run":%d,"metric":%q,"value":%g,"unit":%q}`,
		device, device, ap, target, prec, run, metric, value, unit)
}

// The core fixture: device "Pixel" cell (npu,fp32) has two runs where the
// FASTEST run (run 0, latency 5) has snr 15 (<= 20 dB) and the slower run
// (run 1, latency 8) has snr 30. So speed picks 5.0, auto and accuracy pick
// 8.0 — the case the brief demands where auto-pick != speed-pick. A second
// device "Nexus" cell (cpu,fp16) has a single run at latency 12.0/snr 40.
func generalFixture() string {
	recs := []string{
		grec("Pixel", "npu", "NPU_FP32", "fp32", 0, "latency_ms", 5.0, "ms"),
		grec("Pixel", "npu", "NPU_FP32", "fp32", 0, "snr_db", 15.0, "db"),
		grec("Pixel", "npu", "NPU_FP32", "fp32", 1, "latency_ms", 8.0, "ms"),
		grec("Pixel", "npu", "NPU_FP32", "fp32", 1, "snr_db", 30.0, "db"),
		grec("Pixel", "npu", "NPU_FP32", "fp32", 1, "memory_inference_peak_mb", 100.0, "mb"),
		grec("Nexus", "cpu", "CPU_FP16", "fp16", 0, "latency_ms", 12.0, "ms"),
		grec("Nexus", "cpu", "CPU_FP16", "fp16", 0, "snr_db", 40.0, "db"),
	}
	summary := `{"latency_ms":{"fp32":{"min":5,"max":8,"median":8,"avg":6.5},"fp16":{"min":12,"max":12,"median":12,"avg":12},"int8":{"min":0,"max":0,"median":0,"avg":0},"all":{"min":5,"max":12,"median":8,"avg":8.3}},` +
		`"snr_db":{"fp32":{"min":15,"max":30},"fp16":{"min":40,"max":40},"int8":{"min":0,"max":0},"all":{"min":15,"max":40}},` +
		`"memory_mb":{"fp32":{"load_min":0,"load_max":0,"inference_min":100,"inference_max":100},"fp16":{"load_min":0,"load_max":0,"inference_min":0,"inference_max":0},"int8":{"load_min":0,"load_max":0,"inference_min":0,"inference_max":0},"all":{"load_min":0,"load_max":0,"inference_min":100,"inference_max":100}}}`
	return fmt.Sprintf(
		`{"derivation_version":1,"model":{"key":"m_x","version":1},"records":[%s],"summary":%s}`,
		strings.Join(recs, ","), summary)
}

// ---------------------------------------------------------------------------
// general table — the mode-selection rule
// ---------------------------------------------------------------------------

func TestReportGeneralAutoModeObeysPinnedRule(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(200, generalFixture()))

	require.NoError(t, run(t, e, "--no-color", "report", "view", "m_x", "-R", "zetic/whisper"))

	out := e.out.String()
	// Auto pick for Pixel (npu/fp32): the fastest run clearing snr>20 is run 1
	// (latency 8.0), NOT the globally fastest run 0 (latency 5.0, snr 15).
	assert.Contains(t, out, "Pixel", "device row present")
	pixelLine := lineWith(t, out, "Pixel")
	assert.Contains(t, pixelLine, "8.0", "auto pick is the 8.0 run (snr>20), not 5.0")
	assert.NotContains(t, pixelLine, "5.0", "the low-snr fastest run must not fill the auto cell")
	// Nexus only has a cpu/fp16 measurement, so its npu/fp32 cell is "-".
	nexusLine := lineWith(t, out, "Nexus")
	assert.Contains(t, nexusLine, "12.0")
	assert.Contains(t, nexusLine, "-", "Nexus has no npu/fp32 run")
}

func TestReportGeneralSpeedModeDiffersFromAuto(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(200, generalFixture()))

	require.NoError(t, run(t, e, "--no-color", "report", "view", "m_x", "-R", "zetic/whisper", "--mode", "speed"))

	pixelLine := lineWith(t, e.out.String(), "Pixel")
	assert.Contains(t, pixelLine, "5.0", "speed pick is the globally fastest run (latency 5.0)")
}

func TestReportGeneralAccuracyMode(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(200, generalFixture()))

	require.NoError(t, run(t, e, "--no-color", "report", "view", "m_x", "-R", "zetic/whisper", "--mode", "accuracy"))

	pixelLine := lineWith(t, e.out.String(), "Pixel")
	assert.Contains(t, pixelLine, "8.0", "accuracy pick is the highest-snr run (latency 8.0)")
}

func TestReportGeneralHeaderAndSummary(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(200, generalFixture()))

	require.NoError(t, run(t, e, "--no-color", "report", "view", "m_x", "-R", "zetic/whisper"))

	out := e.out.String()
	assert.Contains(t, out, "DEVICE", "header row present")
	assert.Contains(t, out, "CPU/FP16")
	assert.Contains(t, out, "NPU/FP32")
	// cpu/fp16 sorts before npu/fp32 in the stable column order.
	assert.Less(t, strings.Index(out, "CPU/FP16"), strings.Index(out, "NPU/FP32"))
	assert.Contains(t, out, "Summary (latency ms, per precision):")
	assert.Contains(t, out, "fp32  latency 5.0/8.0/8.0", "per-precision latency min/median/max")
	assert.Contains(t, out, "snr 15.0–30.0 dB")
}

func TestReportGeneralDevicesSortedAlphabetically(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(200, generalFixture()))

	require.NoError(t, run(t, e, "--no-color", "report", "view", "m_x", "-R", "zetic/whisper"))

	out := e.out.String()
	assert.Less(t, strings.Index(out, "Nexus"), strings.Index(out, "Pixel"),
		"devices are sorted alphabetically")
}

// ---------------------------------------------------------------------------
// non-TTY TSV — one line per record
// ---------------------------------------------------------------------------

func TestReportGeneralNonTTYRecordPerLine(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(200, generalFixture()))

	require.NoError(t, run(t, e, "report", "view", "m_x", "-R", "zetic/whisper"))

	out := e.out.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Len(t, lines, 7, "one line per raw record, no derived table")
	// The first record: Pixel npu NPU_FP32 fp32 run 0 latency_ms 5.0 ms.
	assert.Equal(t, "Pixel\tPixel\t\t\tnpu\tNPU_FP32\tfp32\t0\tlatency_ms\t5.0\tms", lines[0])
	assert.Empty(t, e.errOut.String())
}

func TestReportGeneralNonTTYPreservesFloat32ValueLosslessly(t *testing.T) {
	e := setup(t)
	body := strings.Replace(generalFixture(), `"value":5`, `"value":1.234`, 1)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "report", "view", "m_x", "-R", "zetic/whisper"))

	first := strings.SplitN(e.out.String(), "\n", 2)[0]
	assert.Equal(t, "Pixel\tPixel\t\t\tnpu\tNPU_FP32\tfp32\t0\tlatency_ms\t1.234\tms", first)
}

func TestReportGeneralJSONByteExact(t *testing.T) {
	e := setup(t)
	body := generalFixture()
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "report", "view", "m_x", "-R", "zetic/whisper", "--json"))
	assert.Equal(t, body+"\n", e.out.String(), "--json preserves API JSON with exactly one trailing newline")
}

// ---------------------------------------------------------------------------
// probe order & --type
// ---------------------------------------------------------------------------

func TestReportProbeGeneral404ThenLLM(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(404, notFound))
	e.reg.Register(httpmock.REST("GET", llmPath), jsonStub(200, llmFixture()))

	require.NoError(t, run(t, e, "--no-color", "report", "view", "m_x", "-R", "zetic/whisper"))

	assert.Contains(t, e.out.String(), "Q4_0", "the LLM report rendered after general 404")
	require.Len(t, e.reg.Requests, 2)
	assert.Contains(t, e.reg.Requests[0].URL.Path, "/reports/general")
	assert.Contains(t, e.reg.Requests[1].URL.Path, "/reports/llm")
}

func TestReportProbeAll404Exits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(404, notFound))
	e.reg.Register(httpmock.REST("GET", llmPath), jsonStub(404, notFound))
	e.reg.Register(httpmock.REST("GET", packagePath), jsonStub(404, notFound))

	err := run(t, e, "report", "view", "m_x", "-R", "zetic/whisper")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "no report available")
	require.Len(t, e.reg.Requests, 3)
}

func TestReportExplicitTypeSkipsProbeAnd404Surfaces(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", llmPath), jsonStub(404, notFound))

	err := run(t, e, "report", "view", "m_x", "-R", "zetic/whisper", "--type", "llm")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	require.Len(t, e.reg.Requests, 1, "explicit --type does not probe other kinds")
	assert.Contains(t, e.reg.Requests[0].URL.Path, "/reports/llm")
}

func TestReportInvalidTypeExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "report", "view", "m_x", "-R", "zetic/whisper", "--type", "bogus")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestReportInvalidModeExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "report", "view", "m_x", "-R", "zetic/whisper", "--mode", "bogus")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestReportModeIsRejectedForExplicitLLMBeforeAPI(t *testing.T) {
	e := setup(t)
	err := run(t, e, "report", "view", "m_x", "-R", "zetic/whisper",
		"--type", "llm", "--mode", "speed")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--mode only applies to general reports")
	assert.Empty(t, e.reg.Requests)
}

func TestReportModeIsRejectedWhenProbeFindsLLM(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(404, notFound))
	e.reg.Register(httpmock.REST("GET", llmPath), jsonStub(200, llmFixture()))

	err := run(t, e, "report", "view", "m_x", "-R", "zetic/whisper", "--mode", "speed")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--mode only applies to general reports")
}

func TestReportForbiddenExits1(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", generalPath), jsonStub(403, forbidden))

	err := run(t, e, "report", "view", "m_x", "-R", "zetic/whisper", "--type", "general")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "token lacks access")
}

func TestReportRequiresRepoExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "report", "view", "m_x")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

// ---------------------------------------------------------------------------
// llm & package
// ---------------------------------------------------------------------------

func llmFixture() string {
	recs := []string{
		`{"device":{"marketing_name":"Pixel","name":"Pixel"},"ap_type":"npu","target":"NPU","quant_type":"Q4_0","dataset":null,"run":0,"metric":"tps","value":42.5,"unit":"tokens_per_s"}`,
		`{"device":{"marketing_name":"Pixel","name":"Pixel"},"ap_type":"npu","target":"NPU","quant_type":"Q8_0","dataset":null,"run":0,"metric":"tps","value":30.0,"unit":"tokens_per_s"}`,
	}
	summary := `{"quants":{"Q4_0":{"best_tps":42.5,"best_ttft_ms":10,"best_memory_mb":100,"best_accuracy":0.9},"Q8_0":{"best_tps":30,"best_ttft_ms":12,"best_memory_mb":120,"best_accuracy":0.95}},` +
		`"accuracy":[{"target":"NPU","quant_type":"Q4_0","dataset":"mmlu","score":0.9},{"target":"NPU","quant_type":"Q8_0","dataset":"mmlu","score":0.95}]}`
	return fmt.Sprintf(`{"derivation_version":1,"model":{"key":"m_x","version":1},"records":[%s],"summary":%s}`,
		strings.Join(recs, ","), summary)
}

func TestReportLLMTable(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", llmPath), jsonStub(200, llmFixture()))

	require.NoError(t, run(t, e, "--no-color", "report", "view", "m_x", "-R", "zetic/whisper", "--type", "llm"))

	out := e.out.String()
	assert.Contains(t, out, "Q4_0")
	assert.Contains(t, out, "Q8_0")
	pixelLine := lineWith(t, out, "Pixel")
	assert.Contains(t, pixelLine, "42.5", "tps cell for Q4_0")
	assert.Contains(t, pixelLine, "30.0", "tps cell for Q8_0")
	assert.Contains(t, out, "Accuracy:")
	assert.Contains(t, out, "mmlu")
}

func packageFixture() string {
	recs := []string{
		`{"device":{"marketing_name":"Pixel","name":"Pixel"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"tps","value":50,"unit":"tokens_per_s"}`,
	}
	summary := `{"auto":{"tps":{"min":50,"max":50,"median":50,"avg":50},"ttft_ms":{"min":10,"max":10,"median":10,"avg":10},"memory_inference_peak_mb":{"min":200,"max":200,"median":200,"avg":200}},` +
		`"speed":{"tps":{"min":50,"max":50,"median":50,"avg":50},"ttft_ms":{"min":10,"max":10,"median":10,"avg":10},"memory_inference_peak_mb":{"min":200,"max":200,"median":200,"avg":200}}}`
	return fmt.Sprintf(`{"derivation_version":1,"model":{"key":"m_x","version":1},"records":[%s],"summary":%s}`,
		strings.Join(recs, ","), summary)
}

func TestReportPackageTable(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", packagePath), jsonStub(200, packageFixture()))

	require.NoError(t, run(t, e, "--no-color", "report", "view", "m_x", "-R", "zetic/whisper", "--type", "package"))

	out := e.out.String()
	assert.Contains(t, out, "MODE")
	assert.Contains(t, out, "auto")
	assert.Contains(t, out, "speed")
	assert.Contains(t, out, "50.0", "median tps")
	assert.Contains(t, out, "200.0", "median memory")
}

func TestReportPackageNonTTYRecordPerLine(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", packagePath), jsonStub(200, packageFixture()))

	require.NoError(t, run(t, e, "report", "view", "m_x", "-R", "zetic/whisper", "--type", "package"))
	lines := strings.Split(strings.TrimRight(e.out.String(), "\n"), "\n")
	require.Len(t, lines, 1)
	assert.Equal(t, "Pixel\tPixel\t\t\tpkg\t1\ttps\t50.0\ttokens_per_s", lines[0])
}

// lineWith returns the single output line containing sub, failing otherwise.
func lineWith(t *testing.T, out, sub string) string {
	t.Helper()
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, sub) {
			return ln
		}
	}
	t.Fatalf("no line containing %q in:\n%s", sub, out)
	return ""
}

// A null summary (nullable-$ref decodes to a zero value) must not drop
// quant columns that exist in the records.
func TestReportLLMTableNullSummaryDerivesColumnsFromRecords(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	body := `{"derivation_version":1,"model":{"key":"m_x","version":1},` +
		`"records":[{"device":{"marketing_name":"Pixel","name":"Pixel"},"quant_type":"Q4_0","run":0,"metric":"tps","value":42.5,"unit":"tokens_per_s"}],` +
		`"summary":null}`
	e.reg.Register(httpmock.REST("GET", llmPath), jsonStub(200, body))

	require.NoError(t, run(t, e, "--no-color", "report", "view", "m_x", "-R", "zetic/whisper", "--type", "llm"))

	out := e.out.String()
	assert.Contains(t, out, "Q4_0", "quant column derived from records despite null summary")
	assert.Contains(t, lineWith(t, out, "Pixel"), "42.5")
}
