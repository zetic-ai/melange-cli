package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
)

// Metric name strings as emitted in package report records.
const (
	metricPackageTps    = "tps"
	metricPackageTtftMs = "ttft_ms"
	// The per-device table headlines inference-peak memory, matching the
	// mode × metric summary's memory column.
	metricPackageMemInferencePeak = "memory_inference_peak_mb"
)

// renderPackage prints the package report: a mode × metric table on a TTY (or
// the per-device table when byDevice is set), or the flat one-record-per-line
// TSV otherwise. byDevice affects only the human table; TSV and --json are
// unchanged.
func renderPackage(ios *iostreams.IOStreams, body []byte, human, byDevice bool) error {
	var resp gen.PackageReportResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decoding package report: %w", err)
	}
	if !human {
		return packageTSV(ios, resp.Records)
	}
	if byDevice {
		return packageByDevice(ios, resp.Records)
	}
	return packageTable(ios, &resp.Summary)
}

// packageTSV emits one record per line: device fields, package, config id,
// metric, value, unit.
func packageTSV(ios *iostreams.IOStreams, records []gen.PackageReportRecord) error {
	tp := tableprinter.New(ios)
	for _, r := range records {
		tp.AddField(deref(r.Device.MarketingName))
		tp.AddField(deref(r.Device.Name))
		tp.AddField(deref(r.Device.Soc))
		tp.AddField(deref(r.Device.Os))
		tp.AddField(deref(r.RunConfiguration.Package))
		tp.AddField(strconv.Itoa(r.RunConfiguration.Id))
		tp.AddField(string(r.Metric))
		tp.AddField(formatRawFloat(r.Value))
		tp.AddField(string(r.Unit))
		tp.EndRow()
	}
	return tp.Render()
}

// packageTable renders the mode × metric table (median values) from the
// per-mode summary aggregates. auto and speed are the same selection in v1.
func packageTable(ios *iostreams.IOStreams, s *gen.PackageReportSummary) error {
	modes := []struct {
		name string
		agg  *gen.PackageModeAggregates
	}{
		{"auto", s.Auto},
		{"speed", s.Speed},
	}
	tp := tableprinter.New(ios)
	tp.HeaderRow("mode", "tps", "ttft_ms", "mem_inf_peak_mb")
	for _, m := range modes {
		if m.agg == nil {
			continue
		}
		tp.AddField(m.name)
		tp.AddField(statMedian(m.agg.Tps))
		tp.AddField(statMedian(m.agg.TtftMs))
		tp.AddField(statMedian(m.agg.MemoryInferencePeakMb))
		tp.EndRow()
	}
	return tp.Render()
}

// statMedian renders a stat's median, or "-" for an absent stat.
func statMedian(st *gen.ReportStats) string {
	if st == nil {
		return "-"
	}
	return formatFloat(st.Median)
}

// packageDeviceRow accumulates one device's best-observed package metrics.
type packageDeviceRow struct {
	soc     string
	tps     float32
	hasTps  bool
	ttft    float32
	hasTtft bool
	mem     float32
	hasMem  bool
}

// packageByDevice renders one row per device: DEVICE, SOC, TPS, TTFT_ms,
// MEM_MB (inference peak). Each metric is the device's best observed value —
// max tps, min ttft, min inference-peak memory — taken independently per
// metric, the same per-cell best rule the llm table uses. Rows sort by device
// identity (marketing_name, falling back to name). An absent metric renders
// "-"; a memory of 0 (unmeasured on that SoC, e.g. a wearable) also reads "-".
func packageByDevice(ios *iostreams.IOStreams, records []gen.PackageReportRecord) error {
	rows := map[string]*packageDeviceRow{}
	for _, r := range records {
		dev := deviceKey(r.Device)
		row := rows[dev]
		if row == nil {
			row = &packageDeviceRow{}
			rows[dev] = row
		}
		if row.soc == "" {
			row.soc = deref(r.Device.Soc)
		}
		switch string(r.Metric) {
		case metricPackageTps:
			if !row.hasTps || r.Value > row.tps {
				row.tps, row.hasTps = r.Value, true
			}
		case metricPackageTtftMs:
			if !row.hasTtft || r.Value < row.ttft {
				row.ttft, row.hasTtft = r.Value, true
			}
		case metricPackageMemInferencePeak:
			// A 0 means the SoC did not report peak memory; treat it as absent
			// so the cell reads "-" rather than a misleading 0.0.
			if r.Value > 0 && (!row.hasMem || r.Value < row.mem) {
				row.mem, row.hasMem = r.Value, true
			}
		}
	}

	tp := tableprinter.New(ios)
	tp.HeaderRow("device", "soc", "tps", "ttft_ms", "mem_mb")
	for _, dev := range sortedRowKeys(rows) {
		row := rows[dev]
		tp.AddField(displayDevice(dev))
		tp.AddField(orDash(row.soc))
		tp.AddField(optionalStat(row.hasTps, row.tps))
		tp.AddField(optionalStat(row.hasTtft, row.ttft))
		tp.AddField(optionalStat(row.hasMem, row.mem))
		tp.EndRow()
	}
	return tp.Render()
}

// optionalStat renders a metric value, or "-" when the device carried none.
func optionalStat(has bool, v float32) string {
	if !has {
		return "-"
	}
	return formatFloat(v)
}

// sortedRowKeys returns the per-device row keys sorted ascending.
func sortedRowKeys(m map[string]*packageDeviceRow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
