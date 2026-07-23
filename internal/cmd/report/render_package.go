package report

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
)

// renderPackage prints the package report: a mode × metric table on a TTY, or
// the flat one-record-per-line TSV otherwise.
func renderPackage(ios *iostreams.IOStreams, body []byte, isTTY bool) error {
	var resp gen.PackageReportResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decoding package report: %w", err)
	}
	if !isTTY {
		return packageTSV(ios, resp.Records)
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
