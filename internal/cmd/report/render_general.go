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

// apPrecisionOrder is the stable column order: ap_type (cpu, gpu, npu) crossed
// with precision (fp32, fp16, int8). Only the pairs present in records appear.
var (
	apTypeOrder    = []string{"cpu", "gpu", "npu"}
	precisionOrder = []string{"fp32", "fp16", "int8"}
)

// column identifies an (ap_type, precision) table column.
type column struct {
	apType    string
	precision string
}

func (c column) header() string { return c.apType + "/" + c.precision }

// renderGeneral prints the general report: the dashboard table on a TTY, or the
// flat one-record-per-line TSV otherwise.
func renderGeneral(ios *iostreams.IOStreams, body []byte, m mode, isTTY bool) error {
	var resp gen.GeneralReportResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decoding general report: %w", err)
	}
	if !isTTY {
		return generalTSV(ios, resp.Records)
	}
	return generalTable(ios, &resp, m)
}

// generalTSV emits one record per line: device fields, ap_type, target,
// precision, run, metric, value, unit.
func generalTSV(ios *iostreams.IOStreams, records []gen.GeneralReportRecord) error {
	tp := tableprinter.New(ios) // non-TTY: raw tab-separated, no header.
	for _, r := range records {
		tp.AddField(deref(r.Device.MarketingName))
		tp.AddField(deref(r.Device.Name))
		tp.AddField(deref(r.Device.Soc))
		tp.AddField(deref(r.Device.Os))
		tp.AddField(deref(r.ApType))
		tp.AddField(deref(r.Target))
		tp.AddField(string(r.Precision))
		tp.AddField(strconv.Itoa(r.Run))
		tp.AddField(string(r.Metric))
		tp.AddField(formatRawFloat(r.Value))
		tp.AddField(string(r.Unit))
		tp.EndRow()
	}
	return tp.Render()
}

// generalTable renders the dashboard table plus the summary block.
func generalTable(ios *iostreams.IOStreams, resp *gen.GeneralReportResponse, m mode) error {
	// Bucket records by device, then by (ap_type, precision) cell.
	type cellKey struct {
		device string
		col    column
	}
	cells := map[cellKey][]gen.GeneralReportRecord{}
	devices := map[string]bool{}
	presentCols := map[column]bool{}
	for _, r := range resp.Records {
		dev := deviceKey(r.Device)
		devices[dev] = true
		col := column{apType: deref(r.ApType), precision: string(r.Precision)}
		presentCols[col] = true
		k := cellKey{device: dev, col: col}
		cells[k] = append(cells[k], r)
	}

	cols := orderedColumns(presentCols)
	devNames := sortedKeys(devices)

	tp := tableprinter.New(ios)
	header := append([]string{"device"}, columnHeaders(cols)...)
	tp.HeaderRow(header...)
	for _, dev := range devNames {
		tp.AddField(displayDevice(dev))
		for _, col := range cols {
			runs := buildRuns(cells[cellKey{device: dev, col: col}])
			win := selectRun(runs, m)
			if win == nil || !win.hasLatency {
				tp.AddField("-")
				continue
			}
			tp.AddField(formatFloat(win.latency))
		}
		tp.EndRow()
	}
	if err := tp.Render(); err != nil {
		return err
	}
	return generalSummary(ios, &resp.Summary)
}

// orderedColumns returns the present columns in the stable documented order:
// ap_type (cpu, gpu, npu) then precision (fp32, fp16, int8).
func orderedColumns(present map[column]bool) []column {
	var cols []column
	for _, ap := range apTypeOrder {
		for _, prec := range precisionOrder {
			c := column{apType: ap, precision: prec}
			if present[c] {
				cols = append(cols, c)
			}
		}
	}
	// Any (ap_type, precision) outside the known enums still shows, appended
	// in a deterministic order so the table never silently drops a column.
	var extras []column
	for c := range present {
		if !knownColumn(c) {
			extras = append(extras, c)
		}
	}
	sort.Slice(extras, func(i, j int) bool {
		if extras[i].apType != extras[j].apType {
			return extras[i].apType < extras[j].apType
		}
		return extras[i].precision < extras[j].precision
	})
	return append(cols, extras...)
}

func knownColumn(c column) bool {
	known := func(v string, set []string) bool {
		for _, s := range set {
			if s == v {
				return true
			}
		}
		return false
	}
	return known(c.apType, apTypeOrder) && known(c.precision, precisionOrder)
}

func columnHeaders(cols []column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.header()
	}
	return out
}

// displayDevice renders a device group name, using a placeholder for the
// blank-identity group.
func displayDevice(dev string) string {
	if dev == "" {
		return "(unknown)"
	}
	return dev
}

// generalSummary prints the per-precision summary block from the response
// summary: latency min/median/max, SNR range, memory range.
func generalSummary(ios *iostreams.IOStreams, s *gen.GeneralReportSummary) error {
	type summaryRow struct{ precision, latency, snr, memory string }
	var rows []summaryRow
	for _, prec := range precisionOrder {
		lat := latencyStats(s.LatencyMs, prec)
		snr := snrRange(s.SnrDb, prec)
		mem := memoryRange(s.MemoryMb, prec)
		if lat == "" && snr == "" && mem == "" {
			continue
		}
		rows = append(rows, summaryRow{prec, orDash(lat), orDash(snr), orDash(mem)})
	}
	if len(rows) == 0 {
		return nil
	}

	if _, err := fmt.Fprintln(ios.Out, "\nSummary (latency ms, per precision):"); err != nil {
		return err
	}
	tp := tableprinter.New(ios)
	tp.HeaderRow("precision", "latency min/med/max", "snr", "memory")
	for _, r := range rows {
		tp.AddField(r.precision)
		tp.AddField(r.latency)
		tp.AddField(r.snr)
		tp.AddField(r.memory)
		tp.EndRow()
	}
	return tp.Render()
}

func latencyStats(s gen.GeneralLatencySummary, prec string) string {
	st := pickStats(s, prec)
	if st == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s/%s",
		formatFloat(st.Min), formatFloat(st.Median), formatFloat(st.Max))
}

func snrRange(s gen.GeneralSnrSummary, prec string) string {
	mm := pickMinMax(s, prec)
	if mm == nil {
		return ""
	}
	return fmt.Sprintf("%s–%s dB", formatFloat(mm.Min), formatFloat(mm.Max))
}

func memoryRange(s gen.GeneralMemorySummary, prec string) string {
	mb := pickMemory(s, prec)
	if mb == nil {
		return ""
	}
	return fmt.Sprintf("load %s–%s / inf %s–%s MB",
		formatOptionalFloat(mb.LoadMin), formatOptionalFloat(mb.LoadMax),
		formatOptionalFloat(mb.InferenceMin), formatOptionalFloat(mb.InferenceMax))
}

// pickStats/pickMinMax/pickMemory select a nullable per-precision bucket.
func pickStats(s gen.GeneralLatencySummary, prec string) *gen.ReportStats {
	switch prec {
	case "fp32":
		return s.Fp32
	case "fp16":
		return s.Fp16
	case "int8":
		return s.Int8
	default:
		return nil
	}
}

func pickMinMax(s gen.GeneralSnrSummary, prec string) *gen.ReportMinMax {
	switch prec {
	case "fp32":
		return s.Fp32
	case "fp16":
		return s.Fp16
	case "int8":
		return s.Int8
	default:
		return nil
	}
}

func pickMemory(s gen.GeneralMemorySummary, prec string) *gen.ReportMemoryBounds {
	switch prec {
	case "fp32":
		return s.Fp32
	case "fp16":
		return s.Fp16
	case "int8":
		return s.Int8
	default:
		return nil
	}
}

func formatOptionalFloat(value *float32) string {
	if value == nil {
		return "-"
	}
	return formatFloat(*value)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// sortedKeys returns the map keys sorted ascending.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
