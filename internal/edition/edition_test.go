package edition_test

import (
	"encoding/json"
	"errors"
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

func TestQualcommLLMReportRetainsAccuracyButRequiresDeviceMeasurements(t *testing.T) {
	body := []byte(`{
  "derivation_version":3,
  "model":{"key":"llm1","version":2},
  "records":[
    {"device":{"marketing_name":"Samsung Galaxy S24 Ultra","name":"SM-S928U","soc":"SM8650","os":"14"},"ap_type":"npu","variant":"v1","quant_type":"q4","dataset":null,"run":0,"metric":"tps","value":20,"unit":"tokens_per_s"},
    {"device":null,"ap_type":null,"variant":null,"quant_type":"q4","dataset":"arc","run":0,"metric":"accuracy_score","value":0.75,"unit":"score"},
    {"device":null,"ap_type":null,"variant":null,"quant_type":"q4","dataset":null,"run":0,"metric":"tps","value":999,"unit":"tokens_per_s"}
  ],
  "summary":{"quants":{"q4":{"best_tps":20,"best_ttft_ms":null,"best_memory_mb":null,"best_accuracy":0.75}},"accuracy":[{"quant_type":"q4","dataset":"arc","score":0.75}]}
}`)

	got, err := edition.Qualcomm().FilterReport("llm", body)

	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(got, &envelope))
	assert.Len(t, envelope["records"].([]any), 2)
	meta := envelope["qualcomm_filter"].(map[string]any)
	assert.Equal(t, float64(1), meta["hidden_unclassified_records"])

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
