package edition_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/edition"
)

func TestStandardReportPolicyPreservesResponseBytes(t *testing.T) {
	body := []byte("{\"summary\":{},\"records\":[]}\n")

	got, err := edition.Standard().FilterReport("general", body)

	require.NoError(t, err)
	assert.Equal(t, body, got)
}

func TestQualcommGeneralReportFiltersFixedFleetAndRecomputesSummary(t *testing.T) {
	body := []byte(`{
  "derivation_version": 3,
  "model": {"key": "m1", "version": 1},
  "records": [
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"ap_type":"npu","variant":"a","precision":"fp16","run":0,"metric":"latency_ms","value":8,"unit":"ms"},
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"ap_type":"npu","variant":"a","precision":"fp16","run":0,"metric":"snr_db","value":30,"unit":"db"},
    {"device":{"marketing_name":"Google Pixel 10","name":"Pixel 10","soc":"Tensor G5","os":"16"},"ap_type":"npu","variant":"b","precision":"fp16","run":0,"metric":"latency_ms","value":4,"unit":"ms"},
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"other","soc":"SM9999","os":"15"},"ap_type":"npu","variant":"c","precision":"fp16","run":0,"metric":"latency_ms","value":2,"unit":"ms"}
  ],
  "summary": {"latency_ms":{"all":{"min":2,"max":8,"median":4,"avg":4.67},"fp16":{"min":2,"max":8,"median":4,"avg":4.67},"fp32":null,"int8":null},"snr_db":{"all":{"min":30,"max":30},"fp16":{"min":30,"max":30},"fp32":null,"int8":null},"memory_mb":{"all":null,"fp16":null,"fp32":null,"int8":null}}
}`)

	got, err := edition.Qualcomm().FilterReport("general", body)

	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(got, &envelope))
	records := envelope["records"].([]any)
	require.Len(t, records, 2)
	for _, raw := range records {
		device := raw.(map[string]any)["device"].(map[string]any)
		assert.Equal(t, "Samsung Galaxy S25", device["marketing_name"])
		assert.Equal(t, "SM8750", device["soc"])
	}
	meta := envelope["qualcomm_filter"].(map[string]any)
	assert.Equal(t, "fixed-fleet-v1", meta["strategy"])
	assert.Equal(t, float64(1), meta["matched_devices"])
	assert.Equal(t, float64(1), meta["hidden_non_qualcomm_records"])
	assert.Equal(t, float64(1), meta["hidden_unclassified_records"])

	summary := envelope["summary"].(map[string]any)
	latency := summary["latency_ms"].(map[string]any)["fp16"].(map[string]any)
	assert.Equal(t, float64(8), latency["min"])
	assert.Equal(t, float64(8), latency["max"])
	assert.Equal(t, float64(8), latency["median"])
	assert.Equal(t, float64(8), latency["avg"])
}

func TestQualcommFixedFleetIncludesEveryApprovedPairAndFailsClosed(t *testing.T) {
	approved := [][2]string{
		{"Samsung Galaxy A36", "SM6475"},
		{"Samsung Galaxy S20 (Unlocked)", "SM8250"},
		{"Samsung Galaxy S22 5G", "SM8450"},
		{"Samsung Galaxy S22 Ultra 5G", "SM8450"},
		{"Samsung Galaxy S23", "SM8550"},
		{"Samsung Galaxy S23 Ultra", "SM8550"},
		{"Samsung Galaxy S24", "SM8650"},
		{"Samsung Galaxy S24 Ultra", "SM8650"},
		{"Samsung Galaxy S25", "SM8750"},
		{"Samsung Galaxy S25 Ultra", "SM8750"},
		{"Samsung Galaxy Tab S8", "SM8450"},
		{"Samsung Galaxy Tab S9", "SM8550"},
		{"Xiaomi 12 Pro", "SM8450"},
		{"Xiaomi 13 Pro", "SM8550"},
		{"Qualcomm SW6100 Wearable", "SW6100"},
	}
	excluded := [][2]string{
		{"Apple iPhone 16 Pro", "A18 Pro"},
		{"Google Pixel 10", "Tensor G5"},
		{"Samsung Galaxy A15", "MT6835"},
		{"Samsung Galaxy A35", "s5e8835"},
		{"Samsung Galaxy S25", "SM8650"}, // approved name, mismatched SoC
		{"Unreviewed Prototype", "SM9999"},
	}

	records := make([]map[string]any, 0, len(approved)+len(excluded))
	for _, pair := range append(approved, excluded...) {
		records = append(records, map[string]any{
			"device":    map[string]any{"marketing_name": pair[0], "soc": pair[1]},
			"precision": "fp16", "metric": "latency_ms", "value": 1,
		})
	}
	body, err := json.Marshal(map[string]any{
		"derivation_version": 3,
		"model":              map[string]any{"key": "m1", "version": 1},
		"records":            records,
		"summary":            map[string]any{},
	})
	require.NoError(t, err)

	got, err := edition.Qualcomm().FilterReport("general", body)
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(got, &envelope))
	assert.Len(t, envelope["records"].([]any), len(approved))
	meta := envelope["qualcomm_filter"].(map[string]any)
	assert.Equal(t, float64(len(approved)), meta["matched_devices"])
	assert.Equal(t, float64(4), meta["hidden_non_qualcomm_records"])
	assert.Equal(t, float64(2), meta["hidden_unclassified_records"])
}

func TestQualcommLLMReportRetainsDevicelessRecordsButRequiresDeviceMeasurements(t *testing.T) {
	body := []byte(`{
  "derivation_version":3,
  "model":{"key":"llm1","version":2},
  "records":[
    {"device":{"marketing_name":"Samsung Galaxy S24 Ultra","name":"SM-S928U","soc":"SM8650","os":"14"},"ap_type":"npu","variant":"v1","quant_type":"q4","dataset":null,"run":0,"metric":"tps","value":20,"unit":"tokens_per_s"},
    {"device":null,"ap_type":null,"variant":null,"quant_type":"q4","dataset":"arc","run":0,"metric":"accuracy_score","value":0.75,"unit":"score"},
    {"device":null,"ap_type":null,"variant":null,"quant_type":"q4","dataset":null,"run":0,"metric":"model_size_bytes","value":2000000000,"unit":"bytes"}
  ],
  "summary":{"quants":{"q4":{"best_tps":20,"best_ttft_ms":null,"best_memory_mb":null,"best_accuracy":0.75,"best_perplexity":null}},"accuracy":[{"quant_type":"q4","dataset":"arc","score":0.75}],"has_perplexity_attempt":true,"ppl_min_scored_tokens":201}
}`)

	got, err := edition.Qualcomm().FilterReport("llm", body)

	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(got, &envelope))
	// ALL device-less records survive — model_size_bytes rows included, not
	// only accuracy_score (the pre-fix filter dropped them).
	assert.Len(t, envelope["records"].([]any), 3)
	meta := envelope["qualcomm_filter"].(map[string]any)
	assert.Equal(t, float64(0), meta["hidden_unclassified_records"])

	// The report-global policy facts copy through from the served summary.
	summary := envelope["summary"].(map[string]any)
	assert.Equal(t, true, summary["has_perplexity_attempt"])
	assert.Equal(t, float64(201), summary["ppl_min_scored_tokens"])

	accuracyOnly := []byte(`{"derivation_version":3,"model":{"key":"llm1","version":2},"records":[{"device":null,"ap_type":null,"variant":null,"quant_type":"q4","dataset":"arc","run":0,"metric":"accuracy_score","value":0.75,"unit":"score"}],"summary":{"quants":{},"accuracy":[]}}`)
	_, err = edition.Qualcomm().FilterReport("llm", accuracyOnly)
	assert.ErrorIs(t, err, edition.ErrNoQualcommMeasurements)
}

func TestQualcommTargetsHideOtherVendorsAndRetainUnscopedArtifacts(t *testing.T) {
	body := []byte(`{"results":[
  {"target_id":"q","kind":"general","precision":"fp16","quant_type":null,"compatibility":{"soc_manufacturer":"Qualcomm","soc_model":"SM8750","os":"android","ap_types":["npu"]},"download_size":10},
  {"target_id":"a","kind":"general","precision":"fp16","quant_type":null,"compatibility":{"soc_manufacturer":"Apple","soc_model":"A18","os":"ios","ap_types":["gpu"]},"download_size":11},
  {"target_id":"u","kind":"llm","precision":null,"quant_type":"q4","compatibility":null,"download_size":12}
],"count":3}`)

	got, err := edition.Qualcomm().FilterTargets(body)

	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(got, &envelope))
	assert.Equal(t, float64(2), envelope["count"])
	results := envelope["results"].([]any)
	assert.Equal(t, "q", results[0].(map[string]any)["target_id"])
	assert.Equal(t, "u", results[1].(map[string]any)["target_id"])
	meta := envelope["qualcomm_filter"].(map[string]any)
	assert.Equal(t, float64(1), meta["retained_unscoped_targets"])
	assert.Equal(t, float64(1), meta["hidden_non_qualcomm_targets"])
}

func TestQualcommDeploymentOptionsAlwaysDefaultToKotlin(t *testing.T) {
	body := []byte(`{"guide_version":1,"default_language":"ios-swift","default_inference_mode":"auto","languages":[{"id":"android-kotlin","label":"Android (Kotlin)","code_language":"kotlin"},{"id":"ios-swift","label":"iOS (Swift)","code_language":"swift"}],"inference_modes":[]}`)

	got, err := edition.Qualcomm().FilterDeploymentOptions(body)
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(got, &envelope))
	assert.Equal(t, "android-kotlin", envelope["default_language"])
}

func TestQualcommReportRejectsUnsupportedKind(t *testing.T) {
	_, err := edition.Qualcomm().FilterReport("unknown", []byte(`{}`))
	assert.Error(t, err)
	assert.False(t, errors.Is(err, edition.ErrNoQualcommMeasurements))
}

// TestQualcommPackageQualityCopiesThroughWhenNothingFiltered proves the
// copy-through path: the filter only ever REMOVES records, so a fleet that
// keeps every device of the real shared fixture drops no quality record, and
// the served pooled_vision_accuracy, scored_images and best_perplexity must
// survive byte-for-byte rather than be re-derived.
func TestQualcommPackageQualityCopiesThroughWhenNothingFiltered(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "openapi", "fixtures", "get_package_report.json"))
	require.NoError(t, err)
	var fixture struct {
		Response struct {
			Body json.RawMessage `json:"body"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))

	// The fixture's device carries the marketing SoC name, which the shipped
	// reviewed fleet does not list; register the pair so every fixture device
	// is kept and the recomputation covers the full served record set.
	defer edition.AddQualcommFleetDeviceForTest("Samsung Galaxy S25", "Snapdragon 8 Elite")()

	got, err := edition.Qualcomm().FilterReport("package", fixture.Response.Body)
	require.NoError(t, err)

	served := summaryQualityLiterals(t, fixture.Response.Body)
	recomputed := summaryQualityLiterals(t, got)
	for _, field := range []string{"pooled_vision_accuracy", "scored_images", "best_perplexity"} {
		assert.Equal(t, served[field], recomputed[field], field)
	}

	var envelope map[string]any
	require.NoError(t, json.Unmarshal(got, &envelope))
	meta := envelope["qualcomm_filter"].(map[string]any)
	assert.Equal(t, float64(0), meta["hidden_non_qualcomm_records"], "the fleet keeps every fixture device")
	assert.Equal(t, float64(0), meta["hidden_unclassified_records"])
}

// summaryQualityLiterals extracts summary quality fields as raw JSON literals
// so the equality assertion is byte-for-byte, not float-tolerant.
func summaryQualityLiterals(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var doc struct {
		Summary map[string]json.RawMessage `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(body, &doc))
	out := map[string]string{}
	for _, field := range []string{"pooled_vision_accuracy", "scored_images", "best_perplexity"} {
		out[field] = string(doc.Summary[field])
	}
	return out
}

// TestQualcommPackageRepoolsVisionAccuracyOverTheVisibleFleet exercises the
// drop path: once a quality record is filtered out the served summary
// describes devices this edition hides, so the fields are re-derived over the
// kept runs only. The kept pair (15.4 / 160) is precision-sensitive — pooling
// it from the float32 record values and rounding with math.Round(x*1e4)/1e4
// answers 0.0962 instead of the backend's 0.0963.
func TestQualcommPackageRepoolsVisionAccuracyOverTheVisibleFleet(t *testing.T) {
	body := []byte(`{
  "derivation_version":4,
  "model":{"key":"p1","version":1},
  "records":[
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"tps","value":30,"unit":"tokens_per_s"},
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"perplexity","value":12.5,"unit":"ppl"},
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"vision_expected_correct","value":15.4,"unit":"count"},
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"scored_images","value":160,"unit":"count"},
    {"device":{"marketing_name":"Google Pixel 10","name":"Pixel 10","soc":"Tensor G5","os":"16"},"run_configuration":{"package":"pkg","id":2,"configuration":null},"metric":"perplexity","value":9.5,"unit":"ppl"},
    {"device":{"marketing_name":"Google Pixel 10","name":"Pixel 10","soc":"Tensor G5","os":"16"},"run_configuration":{"package":"pkg","id":2,"configuration":null},"metric":"vision_expected_correct","value":29.36,"unit":"count"},
    {"device":{"marketing_name":"Google Pixel 10","name":"Pixel 10","soc":"Tensor G5","os":"16"},"run_configuration":{"package":"pkg","id":2,"configuration":null},"metric":"scored_images","value":45,"unit":"count"}
  ],
  "summary":{"auto":null,"speed":null,"best_perplexity":9.5,"pooled_vision_accuracy":0.2183,"scored_images":205,"has_perplexity_attempt":true,"has_vision_accuracy_attempt":true,"ppl_min_scored_tokens":201}
}`)

	got, err := edition.Qualcomm().FilterReport("package", body)

	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(got, &envelope))
	summary := envelope["summary"].(map[string]any)
	// Σnumerator / Σimages over the KEPT run only: 15.4 / 160.
	assert.Equal(t, 0.0963, summary["pooled_vision_accuracy"])
	assert.Equal(t, float64(160), summary["scored_images"])
	assert.Equal(t, 12.5, summary["best_perplexity"], "the hidden device's lower perplexity must not leak")
	assert.Equal(t, true, summary["has_vision_accuracy_attempt"], "report-global facts still copy through")
}

// TestQualcommPackageSumsRunsThatShareOnePublicIdentity covers the key
// collision the public record cannot rule out: the backend separates runs by
// the device primary key, which no record publishes, so two distinct runs can
// land on one public (device, package, run_configuration) key. Both pairs must
// be pooled — overwriting a metric map would silently drop one run.
func TestQualcommPackageSumsRunsThatShareOnePublicIdentity(t *testing.T) {
	body := []byte(`{
  "derivation_version":4,
  "model":{"key":"p1","version":1},
  "records":[
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"tps","value":30,"unit":"tokens_per_s"},
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"vision_expected_correct","value":15.4,"unit":"count"},
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"scored_images","value":160,"unit":"count"},
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"vision_expected_correct","value":4.1,"unit":"count"},
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"scored_images","value":16,"unit":"count"},
    {"device":{"marketing_name":"Google Pixel 10","name":"Pixel 10","soc":"Tensor G5","os":"16"},"run_configuration":{"package":"pkg","id":2,"configuration":null},"metric":"vision_expected_correct","value":29.36,"unit":"count"},
    {"device":{"marketing_name":"Google Pixel 10","name":"Pixel 10","soc":"Tensor G5","os":"16"},"run_configuration":{"package":"pkg","id":2,"configuration":null},"metric":"scored_images","value":45,"unit":"count"}
  ],
  "summary":{"auto":null,"speed":null,"best_perplexity":null,"pooled_vision_accuracy":0.2183,"scored_images":221,"has_perplexity_attempt":false,"has_vision_accuracy_attempt":true,"ppl_min_scored_tokens":201}
}`)

	got, err := edition.Qualcomm().FilterReport("package", body)

	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(got, &envelope))
	summary := envelope["summary"].(map[string]any)
	// (15.4 + 4.1) / (160 + 16) — both colliding runs, not just the last one.
	assert.Equal(t, 0.1108, summary["pooled_vision_accuracy"])
	assert.Equal(t, float64(176), summary["scored_images"])
}

func TestQualcommPackageCopiesPolicyFactsWhenFilterDropsAllQualityRecords(t *testing.T) {
	// The only perplexity/vision records ride on a non-Qualcomm device; a
	// Qualcomm tps record keeps the report alive. The recomputed values go
	// null, but the report-global attempt flags and threshold COPY through.
	body := []byte(`{
  "derivation_version":4,
  "model":{"key":"p1","version":1},
  "records":[
    {"device":{"marketing_name":"Samsung Galaxy S25","name":"SM-S931U1","soc":"SM8750","os":"15"},"run_configuration":{"package":"pkg","id":1,"configuration":null},"metric":"tps","value":30,"unit":"tokens_per_s"},
    {"device":{"marketing_name":"Google Pixel 10","name":"Pixel 10","soc":"Tensor G5","os":"16"},"run_configuration":{"package":"pkg","id":2,"configuration":null},"metric":"perplexity","value":11.03,"unit":"perplexity"},
    {"device":{"marketing_name":"Google Pixel 10","name":"Pixel 10","soc":"Tensor G5","os":"16"},"run_configuration":{"package":"pkg","id":2,"configuration":null},"metric":"vision_expected_correct","value":29.36,"unit":"count"},
    {"device":{"marketing_name":"Google Pixel 10","name":"Pixel 10","soc":"Tensor G5","os":"16"},"run_configuration":{"package":"pkg","id":2,"configuration":null},"metric":"scored_images","value":45,"unit":"count"}
  ],
  "summary":{"auto":null,"speed":null,"best_perplexity":11.03,"pooled_vision_accuracy":0.6524,"scored_images":45,"has_perplexity_attempt":true,"has_vision_accuracy_attempt":true,"ppl_min_scored_tokens":201}
}`)

	got, err := edition.Qualcomm().FilterReport("package", body)

	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(got, &envelope))
	summary := envelope["summary"].(map[string]any)
	assert.Nil(t, summary["best_perplexity"], "no retained perplexity record")
	assert.Nil(t, summary["pooled_vision_accuracy"], "no retained vision pair")
	assert.Nil(t, summary["scored_images"])
	assert.Equal(t, true, summary["has_perplexity_attempt"], "attempt facts never recomputed from filtered records")
	assert.Equal(t, true, summary["has_vision_accuracy_attempt"])
	assert.Equal(t, float64(201), summary["ppl_min_scored_tokens"])
}
