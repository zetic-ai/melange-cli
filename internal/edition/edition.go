// Package edition defines additive product policies for binaries built from
// the Melange CLI codebase. The zero/default behavior remains the standard
// melange contract; the Qualcomm edition narrows selected presentation paths.
package edition

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
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
	return Policy{programName: "melange-qualcomm", qualcomm: true}
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
	filtered := make([]gen.LlmReportRecord, 0, len(response.Records))
	meta, matched := filterDevices(response.Records,
		func(r gen.LlmReportRecord) *gen.ReportDevice { return r.Device },
		func(r gen.LlmReportRecord) bool { return string(r.Metric) == "accuracy_score" },
		func(r gen.LlmReportRecord) { filtered = append(filtered, r) },
	)
	if len(matched) == 0 {
		return nil, noMeasurementsError(meta)
	}
	response.Records = filtered
	response.Summary = summarizeLLM(filtered)
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
	filtered := make([]gen.PackageReportRecord, 0, len(response.Records))
	meta, matched := filterDevices(response.Records, func(r gen.PackageReportRecord) *gen.ReportDevice { return &r.Device }, nil, func(r gen.PackageReportRecord) { filtered = append(filtered, r) })
	if len(matched) == 0 {
		return nil, noMeasurementsError(meta)
	}
	response.Records = filtered
	response.Summary = summarizePackage(filtered)
	meta.MatchedDevices = len(matched)
	return json.Marshal(struct {
		gen.PackageReportResponse
		QualcommFilter reportFilterMeta `json:"qualcomm_filter"`
	}{response, meta})
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
			All:  stats(generalValues(buckets["all"], "latency_ms")),
			Fp16: stats(generalValues(buckets["fp16"], "latency_ms")),
			Fp32: stats(generalValues(buckets["fp32"], "latency_ms")),
			Int8: stats(generalValues(buckets["int8"], "latency_ms")),
		},
		SnrDb: gen.GeneralSnrSummary{
			All:  minMax(generalValues(buckets["all"], "snr_db")),
			Fp16: minMax(generalValues(buckets["fp16"], "snr_db")),
			Fp32: minMax(generalValues(buckets["fp32"], "snr_db")),
			Int8: minMax(generalValues(buckets["int8"], "snr_db")),
		},
		MemoryMb: gen.GeneralMemorySummary{
			All:  memoryBounds(buckets["all"]),
			Fp16: memoryBounds(buckets["fp16"]),
			Fp32: memoryBounds(buckets["fp32"]),
			Int8: memoryBounds(buckets["int8"]),
		},
	}
}

func generalValues(records []gen.GeneralReportRecord, metric string) []float32 {
	values := []float32{}
	for _, record := range records {
		if string(record.Metric) == metric {
			values = append(values, record.Value)
		}
	}
	return values
}

func stats(values []float32) *gen.ReportStats {
	if len(values) == 0 {
		return nil
	}
	ordered := append([]float32(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	var sum float64
	for _, value := range ordered {
		sum += float64(value)
	}
	return &gen.ReportStats{
		Min: round2(ordered[0]), Max: round2(ordered[len(ordered)-1]),
		Median: round2(ordered[len(ordered)/2]), Avg: round2(float32(sum / float64(len(ordered)))),
	}
}

func minMax(values []float32) *gen.ReportMinMax {
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
	load := append(generalValues(records, "memory_load_peak_mb"), generalValues(records, "memory_load_min_mb")...)
	inference := append(generalValues(records, "memory_inference_peak_mb"), generalValues(records, "memory_inference_min_mb")...)
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

func summarizeLLM(records []gen.LlmReportRecord) gen.LlmReportSummary {
	byQuant := map[string][]gen.LlmReportRecord{}
	accuracy := []gen.LlmAccuracyEntry{}
	for _, record := range records {
		if record.QuantType != nil {
			byQuant[*record.QuantType] = append(byQuant[*record.QuantType], record)
		}
		if string(record.Metric) == "accuracy_score" {
			accuracy = append(accuracy, gen.LlmAccuracyEntry{Dataset: record.Dataset, QuantType: record.QuantType, Score: record.Value})
		}
	}
	quants := map[string]gen.LlmQuantAggregates{}
	for quant, rows := range byQuant {
		quants[quant] = gen.LlmQuantAggregates{
			BestTps:      maxValue(llmValues(rows, "tps")),
			BestTtftMs:   minValue(llmValues(rows, "ttft_ms")),
			BestMemoryMb: minValue(llmValues(rows, "memory_inference_peak_mb")),
			BestAccuracy: maxValue(llmValues(rows, "accuracy_score")),
		}
	}
	return gen.LlmReportSummary{Quants: quants, Accuracy: accuracy}
}

func llmValues(records []gen.LlmReportRecord, metric string) []float32 {
	values := []float32{}
	for _, record := range records {
		if string(record.Metric) == metric {
			values = append(values, record.Value)
		}
	}
	return values
}

func minValue(values []float32) *float32 {
	if len(values) == 0 {
		return nil
	}
	value := values[0]
	for _, candidate := range values[1:] {
		if candidate < value {
			value = candidate
		}
	}
	value = round2(value)
	return &value
}

func maxValue(values []float32) *float32 {
	if len(values) == 0 {
		return nil
	}
	value := values[0]
	for _, candidate := range values[1:] {
		if candidate > value {
			value = candidate
		}
	}
	value = round2(value)
	return &value
}

func summarizePackage(records []gen.PackageReportRecord) gen.PackageReportSummary {
	type runKey struct {
		device, pkg string
		id          int
	}
	runs := map[runKey]map[string]float32{}
	for _, record := range records {
		key := runKey{device: normalize(deref(record.Device.MarketingName)) + "\x00" + normalize(deref(record.Device.Soc)), pkg: deref(record.RunConfiguration.Package), id: record.RunConfiguration.Id}
		if runs[key] == nil {
			runs[key] = map[string]float32{}
		}
		runs[key][string(record.Metric)] = record.Value
	}
	winners := map[string]map[string]float32{}
	for key, metrics := range runs {
		tps, ok := metrics["tps"]
		if !ok {
			continue
		}
		current, exists := winners[key.device]
		if !exists || tps > current["tps"] {
			winners[key.device] = metrics
		}
	}
	if len(winners) == 0 {
		return gen.PackageReportSummary{}
	}
	values := func(metric string) []float32 {
		out := []float32{}
		for _, winner := range winners {
			if value, ok := winner[metric]; ok {
				out = append(out, value)
			}
		}
		return out
	}
	mode := &gen.PackageModeAggregates{
		Tps: stats(values("tps")), TtftMs: stats(values("ttft_ms")),
		MemoryInferencePeakMb: stats(values("memory_inference_peak_mb")),
	}
	copyMode := *mode
	return gen.PackageReportSummary{Auto: mode, Speed: &copyMode}
}

func round2(value float32) float32 {
	return float32(math.Round(float64(value)*100) / 100)
}
