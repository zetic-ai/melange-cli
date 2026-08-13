// Package edition defines additive product policies for binaries built from
// the Melange CLI codebase. The zero/default behavior remains the standard
// melange contract; the Qualcomm edition narrows selected presentation paths.
package edition

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// Record metric names the summaries key on. The general, LLM and package
// paths share one vocabulary, so the literals live here instead of being
// re-spelled at every call site.
const (
	metricLatencyMs             = "latency_ms"
	metricSnrDb                 = "snr_db"
	metricMemoryLoadPeakMb      = "memory_load_peak_mb"
	metricMemoryLoadMinMb       = "memory_load_min_mb"
	metricMemoryInferencePeakMb = "memory_inference_peak_mb"
	metricMemoryInferenceMinMb  = "memory_inference_min_mb"
	metricTps                   = "tps"
	metricTtftMs                = "ttft_ms"
	metricAccuracyScore         = "accuracy_score"
	metricPerplexity            = "perplexity"
	metricVisionExpectedCorrect = "vision_expected_correct"
	metricScoredImages          = "scored_images"
)

// ErrNoQualcommMeasurements means a report carried no approved Qualcomm
// device measurements. Device-less LLM accuracy rows do not satisfy this
// requirement because they are not device benchmarks.
var ErrNoQualcommMeasurements = errors.New("no Qualcomm device measurements found")

// Policy is the behavior selected by one executable.
type Policy struct {
	programName string
	qualcomm    bool
}

// Standard returns the unchanged public Melange behavior.
func Standard() Policy { return Policy{programName: "melange"} }

// Qualcomm returns the curated Qualcomm behavior.
func Qualcomm() Policy {
	return Policy{programName: "melange-qcom", qualcomm: true}
}

// ProgramName is the executable name shown to users.
func (p Policy) ProgramName() string {
	if p.programName == "" {
		return "melange"
	}
	return p.programName
}

// IsQualcomm reports whether the curated policy is active.
func (p Policy) IsQualcomm() bool { return p.qualcomm }

// DeploymentLanguages returns the guide languages exposed by this edition.
func (p Policy) DeploymentLanguages() []string {
	if p.qualcomm {
		return []string{"android-kotlin", "android-java", "flutter"}
	}
	return []string{"android-kotlin", "android-java", "ios-swift", "flutter"}
}

// AllowsDeploymentLanguage reports whether a guide language belongs to this
// edition's curated surface.
func (p Policy) AllowsDeploymentLanguage(language string) bool {
	for _, allowed := range p.DeploymentLanguages() {
		if language == allowed {
			return true
		}
	}
	return false
}

// FilterDeploymentOptions keeps standard response bytes exact and narrows the
// Qualcomm catalog to Android and Flutter guide choices.
func (p Policy) FilterDeploymentOptions(body []byte) ([]byte, error) {
	if !p.qualcomm {
		return body, nil
	}
	var response gen.DeploymentOptionsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding deployment options: %w", err)
	}
	filtered := response.Languages[:0]
	for _, language := range response.Languages {
		if p.AllowsDeploymentLanguage(string(language.Id)) {
			filtered = append(filtered, language)
		}
	}
	response.Languages = filtered
	response.DefaultLanguage = gen.DeploymentOptionsResponseDefaultLanguageAndroidKotlin
	return json.Marshal(response)
}

type reportFilterMeta struct {
	Strategy                  string `json:"strategy"`
	MatchedDevices            int    `json:"matched_devices"`
	HiddenNonQualcommRecords  int    `json:"hidden_non_qualcomm_records"`
	HiddenUnclassifiedRecords int    `json:"hidden_unclassified_records"`
}

type targetFilterMeta struct {
	Strategy                 string `json:"strategy"`
	RetainedUnscopedTargets  int    `json:"retained_unscoped_targets"`
	HiddenNonQualcommTargets int    `json:"hidden_non_qualcomm_targets"`
}

type deviceIdentity struct {
	marketingName string
	soc           string
}

func identity(marketingName, soc string) deviceIdentity {
	return deviceIdentity{
		marketingName: normalize(marketingName),
		soc:           normalize(soc),
	}
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// qualcommFleet is deliberately fail-closed. Add a pair only after the fixed
// benchmark device has been reviewed; marketing name alone is not sufficient
// because one marketed device can exist with more than one SoC.
var qualcommFleet = map[deviceIdentity]struct{}{
	identity("Samsung Galaxy A36", "SM6475"):            {},
	identity("Samsung Galaxy S20 (Unlocked)", "SM8250"): {},
	identity("Samsung Galaxy S22 5G", "SM8450"):         {},
	identity("Samsung Galaxy S22 Ultra 5G", "SM8450"):   {},
	identity("Samsung Galaxy S23", "SM8550"):            {},
	identity("Samsung Galaxy S23 Ultra", "SM8550"):      {},
	identity("Samsung Galaxy S24", "SM8650"):            {},
	identity("Samsung Galaxy S24 Ultra", "SM8650"):      {},
	identity("Samsung Galaxy S25", "SM8750"):            {},
	identity("Samsung Galaxy S25 Ultra", "SM8750"):      {},
	identity("Samsung Galaxy Tab S8", "SM8450"):         {},
	identity("Samsung Galaxy Tab S9", "SM8550"):         {},
	identity("Xiaomi 12 Pro", "SM8450"):                 {},
	identity("Xiaomi 13 Pro", "SM8550"):                 {},
	identity("Qualcomm SW6100 Wearable", "SW6100"):      {},
}

// knownNonQualcommFleet lets the edition distinguish reviewed non-Qualcomm
// devices from genuinely unknown identities in its disclosure metadata.
var knownNonQualcommFleet = map[deviceIdentity]struct{}{
	identity("Apple iPad (2025)", "iPad15,7"):            {},
	identity("Apple iPad Air (2022)", "iPad13,16"):       {},
	identity("Apple iPad Air 11 (2025)", "iPad15,3"):     {},
	identity("Apple iPad Air 13 (2025)", "iPad15,5"):     {},
	identity("Apple iPad Pro 12.9\" (2022)", "iPad14,5"): {},
	identity("Apple iPad mini (2024)", "iPad16,1"):       {},
	identity("Apple iPhone 12", "A14"):                   {},
	identity("Apple iPhone 14", "A15"):                   {},
	identity("Apple iPhone 14 Pro", "A16"):               {},
	identity("Apple iPhone 15", "A16"):                   {},
	identity("Apple iPhone 15 Pro", "A17 Pro"):           {},
	identity("Apple iPhone 16", "A18"):                   {},
	identity("Apple iPhone 16 Pro", "A18 Pro"):           {},
	identity("Apple iPhone 16e", "A18"):                  {},
	identity("Google Pixel 7 Pro", "GS201"):              {},
	identity("Google Pixel 8", "Tensor G3"):              {},
	identity("Google Pixel 8 Pro", "Tensor G3"):          {},
	identity("Google Pixel 9", "Tensor G4"):              {},
	identity("Google Pixel 9 Pro", "Tensor G4"):          {},
	identity("Google Pixel 10", "Tensor G5"):             {},
	identity("Google Pixel 10 Pro", "Tensor G5"):         {},
	identity("Samsung A51", "Exynos 9611"):               {},
	identity("Samsung Galaxy A15", "MT6835"):             {},
	identity("Samsung Galaxy A16", "MT6789V/CD"):         {},
	identity("Samsung Galaxy A25", "s5e8825"):            {},
	identity("Samsung Galaxy A26", "s5e8835"):            {},
	identity("Samsung Galaxy A35", "s5e8835"):            {},
	identity("Samsung Galaxy A54", "s5e8835"):            {},
	identity("Samsung Galaxy A55", "s5e8845"):            {},
	identity("Samsung Galaxy Tab A7 Lite", "MT8768WT"):   {},
	identity("Samsung Galaxy Tab A9", "MT8781V/NA"):      {},
}

type deviceClass int

const (
	deviceUnclassified deviceClass = iota
	deviceQualcomm
	deviceNonQualcomm
)

func classifyDevice(d gen.ReportDevice) deviceClass {
	key := identity(deref(d.MarketingName), deref(d.Soc))
	if _, ok := qualcommFleet[key]; ok {
		return deviceQualcomm
	}
	if _, ok := knownNonQualcommFleet[key]; ok {
		return deviceNonQualcomm
	}
	return deviceUnclassified
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// FilterReport applies the edition's output policy. Standard mode returns the
// exact input bytes. Qualcomm mode returns a filtered, re-summarized envelope.
func (p Policy) FilterReport(kind string, body []byte) ([]byte, error) {
	if !p.qualcomm {
		return body, nil
	}
	switch kind {
	case "general":
		return filterGeneralReport(body)
	case "llm":
		return filterLLMReport(body)
	case "package":
		return filterPackageReport(body)
	default:
		return nil, fmt.Errorf("unsupported report kind %q", kind)
	}
}

func filterGeneralReport(body []byte) ([]byte, error) {
	var response gen.GeneralReportResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding general report: %w", err)
	}
	filtered := make([]gen.GeneralReportRecord, 0, len(response.Records))
	meta, matched := filterDevices(response.Records, func(r gen.GeneralReportRecord) *gen.ReportDevice { return &r.Device }, nil, func(r gen.GeneralReportRecord) { filtered = append(filtered, r) })
	if len(matched) == 0 {
		return nil, noMeasurementsError(meta)
	}
	response.Records = filtered
	response.Summary = summarizeGeneral(filtered)
	meta.MatchedDevices = len(matched)
	return json.Marshal(struct {
		gen.GeneralReportResponse
		QualcommFilter reportFilterMeta `json:"qualcomm_filter"`
	}{response, meta})
}

func filterLLMReport(body []byte) ([]byte, error) {
	var response gen.LlmReportResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding llm report: %w", err)
	}
	exact, err := exactRecords(body, len(response.Records))
	if err != nil {
		return nil, fmt.Errorf("decoding llm report: %w", err)
	}
	kept := make([]int, 0, len(response.Records))
	meta, matched := filterDevices(indexes(len(response.Records)),
		func(i int) *gen.ReportDevice { return response.Records[i].Device },
		// Device-less records (accuracy, model size) are model-level, not
		// device benchmarks: they are retained, never fleet-filtered.
		func(int) bool { return true },
		func(i int) { kept = append(kept, i) },
	)
	if len(matched) == 0 {
		return nil, noMeasurementsError(meta)
	}
	filtered := make([]gen.LlmReportRecord, 0, len(kept))
	for _, i := range kept {
		filtered = append(filtered, response.Records[i])
	}
	response.Records = filtered
	response.Summary = summarizeLLM(pick(exact, kept), response.Summary)
	meta.MatchedDevices = len(matched)
	return json.Marshal(struct {
		gen.LlmReportResponse
		QualcommFilter reportFilterMeta `json:"qualcomm_filter"`
	}{response, meta})
}

func filterPackageReport(body []byte) ([]byte, error) {
	var response gen.PackageReportResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding package report: %w", err)
	}
	exact, err := exactRecords(body, len(response.Records))
	if err != nil {
		return nil, fmt.Errorf("decoding package report: %w", err)
	}
	kept := make([]int, 0, len(response.Records))
	meta, matched := filterDevices(indexes(len(response.Records)),
		func(i int) *gen.ReportDevice { return &response.Records[i].Device },
		nil,
		func(i int) { kept = append(kept, i) },
	)
	if len(matched) == 0 {
		return nil, noMeasurementsError(meta)
	}
	filtered := make([]gen.PackageReportRecord, 0, len(kept))
	for _, i := range kept {
		filtered = append(filtered, response.Records[i])
	}
	response.Records = filtered
	response.Summary = summarizePackage(pick(exact, kept), response.Summary, droppedQualityRecord(exact, kept))
	meta.MatchedDevices = len(matched)
	return json.Marshal(struct {
		gen.PackageReportResponse
		QualcommFilter reportFilterMeta `json:"qualcomm_filter"`
	}{response, meta})
}

// exactRecord is a float64 view of one report record, parsed from the SAME
// `records` array as the generated structs and kept index-aligned with them.
//
// The generated client types `value` as float32 (the width the contract
// declares), which transports a served value fine but cannot re-derive one:
// the backend pools vision accuracy in float64, and float32 inputs move the
// 4-decimal result (15.4/160 lands on 0.0962 instead of 0.0963). Summaries are
// therefore recomputed from this view, never from the generated records.
type exactRecord struct {
	Metric           string           `json:"metric"`
	Value            float64          `json:"value"`
	Dataset          *string          `json:"dataset"`
	QuantType        *string          `json:"quant_type"`
	Device           gen.ReportDevice `json:"device"`
	RunConfiguration exactRunConfig   `json:"run_configuration"`
}

type exactRunConfig struct {
	Id      int     `json:"id"`
	Package *string `json:"package"`
}

func exactValue(r exactRecord) (string, float64) { return r.Metric, r.Value }

// exactRecords parses body's `records` array as the float64 view. The count is
// asserted against the generated slice so the two stay index-aligned: every
// downstream lookup addresses both by the same position.
func exactRecords(body []byte, decoded int) ([]exactRecord, error) {
	var doc struct {
		Records []exactRecord `json:"records"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	if len(doc.Records) != decoded {
		return nil, fmt.Errorf("record view desynchronized: %d exact records for %d decoded records", len(doc.Records), decoded)
	}
	return doc.Records, nil
}

// indexes returns 0..n-1 so filterDevices can run over record positions rather
// than over one record type, which keeps the generated records and their
// float64 view aligned under the same predicate and order.
func indexes(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// pick returns the elements at the given (ascending) positions.
func pick[T any](values []T, positions []int) []T {
	out := make([]T, 0, len(positions))
	for _, i := range positions {
		out = append(out, values[i])
	}
	return out
}

// droppedQualityRecord reports whether the fleet filter removed any record the
// package summary's quality fields derive from. positions is ascending.
func droppedQualityRecord(all []exactRecord, positions []int) bool {
	next := 0
	for i, record := range all {
		if next < len(positions) && positions[next] == i {
			next++
			continue
		}
		switch record.Metric {
		case metricPerplexity, metricVisionExpectedCorrect, metricScoredImages:
			return true
		}
	}
	return false
}

func filterDevices[T any](records []T, device func(T) *gen.ReportDevice, keepUnscoped func(T) bool, keep func(T)) (reportFilterMeta, map[deviceIdentity]struct{}) {
	meta := reportFilterMeta{Strategy: "fixed-fleet-v1"}
	matched := map[deviceIdentity]struct{}{}
	for _, record := range records {
		d := device(record)
		if d == nil {
			if keepUnscoped != nil && keepUnscoped(record) {
				keep(record)
			} else {
				meta.HiddenUnclassifiedRecords++
			}
			continue
		}
		switch classifyDevice(*d) {
		case deviceQualcomm:
			keep(record)
			matched[identity(deref(d.MarketingName), deref(d.Soc))] = struct{}{}
		case deviceNonQualcomm:
			meta.HiddenNonQualcommRecords++
		default:
			meta.HiddenUnclassifiedRecords++
		}
	}
	return meta, matched
}

func noMeasurementsError(meta reportFilterMeta) error {
	return fmt.Errorf("%w (%d non-Qualcomm and %d unclassified records hidden)",
		ErrNoQualcommMeasurements, meta.HiddenNonQualcommRecords, meta.HiddenUnclassifiedRecords)
}

// FilterTargets removes explicitly non-Qualcomm artifacts while retaining
// device-unscoped artifacts used by LLM and universal targets.
func (p Policy) FilterTargets(body []byte) ([]byte, error) {
	if !p.qualcomm {
		return body, nil
	}
	var response gen.ListModelTargetsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding model targets: %w", err)
	}
	meta := targetFilterMeta{Strategy: "soc-manufacturer-v1"}
	filtered := make([]gen.ModelTargetItem, 0, len(response.Results))
	for _, target := range response.Results {
		if target.Compatibility == nil {
			filtered = append(filtered, target)
			meta.RetainedUnscopedTargets++
			continue
		}
		if strings.EqualFold(strings.TrimSpace(deref(target.Compatibility.SocManufacturer)), "Qualcomm") {
			filtered = append(filtered, target)
			continue
		}
		meta.HiddenNonQualcommTargets++
	}
	response.Results = filtered
	response.Count = len(filtered)
	return json.Marshal(struct {
		gen.ListModelTargetsResponse
		QualcommFilter targetFilterMeta `json:"qualcomm_filter"`
	}{response, meta})
}

func summarizeGeneral(records []gen.GeneralReportRecord) gen.GeneralReportSummary {
	buckets := map[string][]gen.GeneralReportRecord{"all": records}
	for _, record := range records {
		precision := string(record.Precision)
		buckets[precision] = append(buckets[precision], record)
	}
	return gen.GeneralReportSummary{
		LatencyMs: gen.GeneralLatencySummary{
			All:  stats(metricValues(buckets["all"], metricLatencyMs, generalValue)),
			Fp16: stats(metricValues(buckets["fp16"], metricLatencyMs, generalValue)),
			Fp32: stats(metricValues(buckets["fp32"], metricLatencyMs, generalValue)),
			Int8: stats(metricValues(buckets["int8"], metricLatencyMs, generalValue)),
		},
		SnrDb: gen.GeneralSnrSummary{
			All:  minMax(metricValues(buckets["all"], metricSnrDb, generalValue)),
			Fp16: minMax(metricValues(buckets["fp16"], metricSnrDb, generalValue)),
			Fp32: minMax(metricValues(buckets["fp32"], metricSnrDb, generalValue)),
			Int8: minMax(metricValues(buckets["int8"], metricSnrDb, generalValue)),
		},
		MemoryMb: gen.GeneralMemorySummary{
			All:  memoryBounds(buckets["all"]),
			Fp16: memoryBounds(buckets["fp16"]),
			Fp32: memoryBounds(buckets["fp32"]),
			Int8: memoryBounds(buckets["int8"]),
		},
	}
}

// metricValues is the one metric collector every summary path uses: value
// reads a record's (metric name, value) pair, so the same helper serves the
// generated general records and the float64 record view alike. Values are
// float64 because that is the width the backend aggregates in.
func metricValues[T any](records []T, metric string, value func(T) (string, float64)) []float64 {
	values := []float64{}
	for _, record := range records {
		if name, v := value(record); name == metric {
			values = append(values, v)
		}
	}
	return values
}

func generalValue(r gen.GeneralReportRecord) (string, float64) {
	return string(r.Metric), float64(r.Value)
}

func stats(values []float64) *gen.ReportStats {
	if len(values) == 0 {
		return nil
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	var sum float64
	for _, value := range ordered {
		sum += value
	}
	return &gen.ReportStats{
		Min: round2(ordered[0]), Max: round2(ordered[len(ordered)-1]),
		Median: round2(ordered[len(ordered)/2]), Avg: round2(sum / float64(len(ordered))),
	}
}

func minMax(values []float64) *gen.ReportMinMax {
	if len(values) == 0 {
		return nil
	}
	min, max := values[0], values[0]
	for _, value := range values[1:] {
		if value < min {
			min = value
		}
		if value > max {
			max = value
		}
	}
	return &gen.ReportMinMax{Min: round2(min), Max: round2(max)}
}

func memoryBounds(records []gen.GeneralReportRecord) *gen.ReportMemoryBounds {
	load := append(metricValues(records, metricMemoryLoadPeakMb, generalValue), metricValues(records, metricMemoryLoadMinMb, generalValue)...)
	inference := append(metricValues(records, metricMemoryInferencePeakMb, generalValue), metricValues(records, metricMemoryInferenceMinMb, generalValue)...)
	if len(load) == 0 && len(inference) == 0 {
		return nil
	}
	result := &gen.ReportMemoryBounds{}
	if mm := minMax(load); mm != nil {
		result.LoadMin, result.LoadMax = &mm.Min, &mm.Max
	}
	if mm := minMax(inference); mm != nil {
		result.InferenceMin, result.InferenceMax = &mm.Min, &mm.Max
	}
	return result
}

// summarizeLLM recomputes per-quant aggregates from the filtered records.
// The attempt flag and reliability threshold are report-global policy facts:
// they COPY through from the served summary, never recomputed from the
// filtered slice.
func summarizeLLM(records []exactRecord, served gen.LlmReportSummary) gen.LlmReportSummary {
	byQuant := map[string][]exactRecord{}
	accuracy := []gen.LlmAccuracyEntry{}
	for _, record := range records {
		if record.QuantType != nil {
			byQuant[*record.QuantType] = append(byQuant[*record.QuantType], record)
		}
		if record.Metric == metricAccuracyScore {
			accuracy = append(accuracy, gen.LlmAccuracyEntry{Dataset: record.Dataset, QuantType: record.QuantType, Score: float32(record.Value)})
		}
	}
	quants := map[string]gen.LlmQuantAggregates{}
	for quant, rows := range byQuant {
		quants[quant] = gen.LlmQuantAggregates{
			BestTps:        maxValue(metricValues(rows, metricTps, exactValue)),
			BestTtftMs:     minValue(metricValues(rows, metricTtftMs, exactValue)),
			BestMemoryMb:   minValue(metricValues(rows, metricMemoryInferencePeakMb, exactValue)),
			BestAccuracy:   maxValue(metricValues(rows, metricAccuracyScore, exactValue)),
			BestPerplexity: minValueExact(metricValues(rows, metricPerplexity, exactValue)),
		}
	}
	return gen.LlmReportSummary{
		Quants:               quants,
		Accuracy:             accuracy,
		HasPerplexityAttempt: served.HasPerplexityAttempt,
		PplMinScoredTokens:   served.PplMinScoredTokens,
	}
}

func minValue(values []float64) *float32 {
	if len(values) == 0 {
		return nil
	}
	rounded := round2(minOf(values))
	return &rounded
}

// minValueExact keeps the published value as served: perplexity minima are
// already rounded by the backend, so re-rounding could only move them.
func minValueExact(values []float64) *float32 {
	if len(values) == 0 {
		return nil
	}
	value := float32(minOf(values))
	return &value
}

func minOf(values []float64) float64 {
	value := values[0]
	for _, candidate := range values[1:] {
		if candidate < value {
			value = candidate
		}
	}
	return value
}

func maxValue(values []float64) *float32 {
	if len(values) == 0 {
		return nil
	}
	value := values[0]
	for _, candidate := range values[1:] {
		if candidate > value {
			value = candidate
		}
	}
	rounded := round2(value)
	return &rounded
}

// summarizePackage rebuilds the package summary over the records this edition
// kept. The attempt flags and the reliability threshold are report-global
// policy facts: they COPY through from the served summary, never recomputed.
//
// The quality fields (best_perplexity, pooled_vision_accuracy, scored_images)
// take one of two paths:
//
//  1. Copy-through, when droppedQuality is false. The Qualcomm filter only
//     ever REMOVES records, so if it dropped no perplexity and no vision
//     record then the served summary already IS the summary of the retained
//     set. Copying it verbatim is exact by construction, which keeps the
//     common all-Qualcomm fleet byte-identical to the backend instead of
//     merely close to it.
//
//  2. Re-derivation, once a quality record WAS dropped. This is a
//     re-derivation over the VISIBLE fleet: it is not expected to equal the
//     served summary, because the served summary covers devices this edition
//     hides. Pooling is Σ(vision_expected_correct)/Σ(scored_images) over runs,
//     rounded the way the backend's round(x, 4) rounds.
func summarizePackage(records []exactRecord, served gen.PackageReportSummary, droppedQuality bool) gen.PackageReportSummary {
	runs, order := groupPackageRuns(records)

	summary := gen.PackageReportSummary{
		BestPerplexity:           served.BestPerplexity,
		PooledVisionAccuracy:     served.PooledVisionAccuracy,
		ScoredImages:             served.ScoredImages,
		HasPerplexityAttempt:     served.HasPerplexityAttempt,
		HasVisionAccuracyAttempt: served.HasVisionAccuracyAttempt,
		PplMinScoredTokens:       served.PplMinScoredTokens,
	}
	if droppedQuality {
		summary.BestPerplexity = minValueExact(metricValues(records, metricPerplexity, exactValue))
		summary.PooledVisionAccuracy, summary.ScoredImages = poolVisionAccuracy(runs, order)
	}

	// PS1: one best-tps winner per device. Devices are walked in record order
	// so a tps tie resolves to the same run on every run of the binary.
	best := map[string]*packageRun{}
	devices := []string{}
	for _, key := range order {
		run := runs[key]
		tps, ok := run.metrics[metricTps]
		if !ok {
			continue
		}
		current, exists := best[key.device]
		if !exists {
			best[key.device] = run
			devices = append(devices, key.device)
			continue
		}
		if tps > current.metrics[metricTps] {
			best[key.device] = run
		}
	}
	if len(devices) == 0 {
		return summary
	}
	values := func(metric string) []float64 {
		out := []float64{}
		for _, device := range devices {
			if value, ok := best[device].metrics[metric]; ok {
				out = append(out, value)
			}
		}
		return out
	}
	mode := &gen.PackageModeAggregates{
		Tps: stats(values(metricTps)), TtftMs: stats(values(metricTtftMs)),
		MemoryInferencePeakMb: stats(values(metricMemoryInferencePeakMb)),
	}
	copyMode := *mode
	summary.Auto, summary.Speed = mode, &copyMode
	return summary
}

// packageRunKey is the finest run identity a PUBLIC package record carries:
// the full device identity plus the run configuration's package and id. The
// backend keys its runs by the device PRIMARY KEY, a summary-only hidden
// field, so this key is still coarser than the backend's and two distinct runs
// can share one key — packageRun therefore accumulates observations instead of
// overwriting them.
type packageRunKey struct {
	device, pkg string
	id          int
}

// packageRun holds one run key's records. metrics is last-write-wins and only
// feeds the per-device best-tps selection, where a collision picks one of two
// equally valid runs. The vision pair must NOT be lost that way, so its
// observations are appended and zipped positionally: the server emits one
// vision_expected_correct and one scored_images per valid run, contiguously,
// so position i of each slice belongs to the same underlying run.
type packageRun struct {
	metrics          map[string]float64
	visionNumerators []float64
	visionImages     []float64
}

func groupPackageRuns(records []exactRecord) (map[packageRunKey]*packageRun, []packageRunKey) {
	runs := map[packageRunKey]*packageRun{}
	order := []packageRunKey{}
	for _, record := range records {
		key := packageRunKey{
			device: deviceKey(record.Device),
			pkg:    deref(record.RunConfiguration.Package),
			id:     record.RunConfiguration.Id,
		}
		run, ok := runs[key]
		if !ok {
			run = &packageRun{metrics: map[string]float64{}}
			runs[key] = run
			order = append(order, key)
		}
		run.metrics[record.Metric] = record.Value
		switch record.Metric {
		case metricVisionExpectedCorrect:
			run.visionNumerators = append(run.visionNumerators, record.Value)
		case metricScoredImages:
			run.visionImages = append(run.visionImages, record.Value)
		}
	}
	return runs, order
}

// deviceKey is the full normalized device identity — all four published
// dimensions, because any subset merges devices the backend keeps apart.
func deviceKey(d gen.ReportDevice) string {
	return strings.Join([]string{
		normalize(deref(d.Name)), normalize(deref(d.MarketingName)),
		normalize(deref(d.Soc)), normalize(deref(d.Os)),
	}, "\x00")
}

// poolVisionAccuracy sums the exact numerators and images across runs and
// rounds the rate the way the backend does.
func poolVisionAccuracy(runs map[packageRunKey]*packageRun, order []packageRunKey) (*float32, *int) {
	var expectedCorrect, scoredImages float64
	for _, key := range order {
		run := runs[key]
		pairs := min(len(run.visionNumerators), len(run.visionImages))
		for i := range pairs {
			expectedCorrect += run.visionNumerators[i]
			scoredImages += run.visionImages[i]
		}
	}
	if scoredImages <= 0 {
		return nil, nil
	}
	pooled := float32(roundDecimal(expectedCorrect/scoredImages, 4))
	total := int(scoredImages)
	return &pooled, &total
}

func round2(value float64) float32 {
	return float32(roundDecimal(value, 2))
}

// roundDecimal rounds to a fixed number of decimal places the way Python's
// round() does — correctly rounded off the exact float64, ties to even.
// Scaling by a power of ten and calling math.Round instead is BOTH biased
// (half away from zero) and inexact (the scaling itself perturbs the value:
// 4.1/16 scales to exactly 2562.5 and rounds up to 0.2563, where the backend
// answers 0.2562). Any value this edition must reproduce from a server-rounded
// computation goes through here.
func roundDecimal(value float64, places int) float64 {
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', places, 64), 64)
	if err != nil {
		return value
	}
	return rounded
}
