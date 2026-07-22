package report

import (
	"sort"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// Metric name strings as emitted in general report records (report_service G2).
const (
	metricLatencyMs           = "latency_ms"
	metricSnrDb               = "snr_db"
	metricMemoryInferencePeak = "memory_inference_peak_mb"
)

// autoSnrThresholdDB is the pinned auto-mode SNR gate: auto picks the fastest
// run whose snr_db is STRICTLY greater than this many dB (derivation rule D8).
const autoSnrThresholdDB = 20.0

// mode is the inference-mode column the table cells are derived for.
type mode string

const (
	modeAuto     mode = "auto"
	modeSpeed    mode = "speed"
	modeAccuracy mode = "accuracy"
)

// run is one benchmark run's metric set, rebuilt from the flat records that
// share a (device, target, ap_type, run) ordinal (derivation rule D9). A run
// enters mode selection only when it has a latency_ms.
type run struct {
	latency    float32
	hasLatency bool
	snr        float32
	hasSNR     bool
	memory     float32
	hasMemory  bool
}

// runKey identifies one run within the pool the table cell is derived over.
// The table re-derives per (device, ap_type, precision), so target and run
// ordinal together identify a run inside that cell's subset (D9's run ordinal
// is scoped to (device, target, ap_type)).
type runKey struct {
	target string
	run    int
}

// deviceKey groups records by the dashboard's device identity: marketing_name,
// falling back to name (derivation rule D8). Blank on both is a distinct group.
func deviceKey(d gen.ReportDevice) string {
	if d.MarketingName != nil && *d.MarketingName != "" {
		return *d.MarketingName
	}
	if d.Name != nil && *d.Name != "" {
		return *d.Name
	}
	return ""
}

// buildRuns folds a set of records into runs keyed by (target, run), attaching
// each run's latency/snr/memory values.
func buildRuns(records []gen.GeneralReportRecord) map[runKey]*run {
	runs := map[runKey]*run{}
	for _, rec := range records {
		k := runKey{target: deref(rec.Target), run: rec.Run}
		r := runs[k]
		if r == nil {
			r = &run{}
			runs[k] = r
		}
		switch rec.Metric {
		case metricLatencyMs:
			r.latency, r.hasLatency = rec.Value, true
		case metricSnrDb:
			r.snr, r.hasSNR = rec.Value, true
		case metricMemoryInferencePeak:
			r.memory, r.hasMemory = rec.Value, true
		}
	}
	return runs
}

// selectRun applies the pinned mode-selection rule (derivation rule D8) to a
// pool of runs and returns the winning run, or nil when no run qualifies for
// the requested mode. Only runs with a latency_ms participate.
func selectRun(runs map[runKey]*run, m mode) *run {
	// Deterministic iteration: sort candidate keys so ties resolve stably.
	keys := make([]runKey, 0, len(runs))
	for k, r := range runs {
		if r.hasLatency {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].target != keys[j].target {
			return keys[i].target < keys[j].target
		}
		return keys[i].run < keys[j].run
	})
	if len(keys) == 0 {
		return nil
	}

	speed := func() *run {
		var best *run
		for _, k := range keys {
			r := runs[k]
			if best == nil || r.latency < best.latency {
				best = r
			}
		}
		return best
	}

	switch m {
	case modeSpeed:
		return speed()
	case modeAccuracy:
		var best *run
		for _, k := range keys {
			r := runs[k]
			if !r.hasSNR {
				continue
			}
			// Highest snr; ties broken by lower latency.
			if best == nil || r.snr > best.snr ||
				(r.snr == best.snr && r.latency < best.latency) {
				best = r
			}
		}
		return best // nil when no run has snr_db.
	case modeAuto:
		var best *run
		for _, k := range keys {
			r := runs[k]
			if !r.hasSNR || r.snr <= autoSnrThresholdDB {
				continue
			}
			if best == nil || r.latency < best.latency {
				best = r
			}
		}
		if best != nil {
			return best
		}
		return speed() // no run clears the SNR gate → the speed pick.
	}
	return nil
}
