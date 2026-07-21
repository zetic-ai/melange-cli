package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
)

const metricTps = "tps"

// renderLLM prints the LLM report: rows=devices, columns=quant_types, cells=tps
// on a TTY, plus a per-dataset accuracy section; flat TSV otherwise.
func renderLLM(ios *iostreams.IOStreams, body []byte, isTTY bool) error {
	var resp gen.LlmReportResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decoding llm report: %w", err)
	}
	if !isTTY {
		return llmTSV(ios, resp.Records)
	}
	return llmTable(ios, &resp)
}

// llmTSV emits one record per line: device fields, ap_type, target, quant_type,
// dataset, run, metric, value, unit.
func llmTSV(ios *iostreams.IOStreams, records []gen.LlmReportRecord) error {
	tp := tableprinter.New(ios)
	for _, r := range records {
		tp.AddField(deref(r.Device.MarketingName))
		tp.AddField(deref(r.Device.Name))
		tp.AddField(deref(r.Device.Brand))
		tp.AddField(deref(r.Device.Soc))
		tp.AddField(deref(r.Device.OsVersion))
		tp.AddField(string(r.ApType))
		tp.AddField(r.Target)
		tp.AddField(r.QuantType)
		tp.AddField(r.Dataset)
		tp.AddField(strconv.Itoa(r.Run))
		tp.AddField(r.Metric)
		tp.AddField(formatFloat(r.Value))
		tp.AddField(r.Unit)
		tp.EndRow()
	}
	return tp.Render()
}

// llmTable renders the device × quant tps table and the accuracy section.
func llmTable(ios *iostreams.IOStreams, resp *gen.LlmReportResponse) error {
	// Highest tps per (device, quant_type) — the device's best generation rate.
	type cellKey struct{ device, quant string }
	best := map[cellKey]float32{}
	devices := map[string]bool{}
	for _, r := range resp.Records {
		if r.Metric != metricTps || r.QuantType == "" {
			continue
		}
		dev := llmDeviceKey(r.Device)
		devices[dev] = true
		k := cellKey{device: dev, quant: r.QuantType}
		if cur, ok := best[k]; !ok || r.Value > cur {
			best[k] = r.Value
		}
	}

	quants := quantOrder(resp.Summary.Quants)
	devNames := sortedKeys(devices)

	tp := tableprinter.New(ios)
	tp.HeaderRow(append([]string{"device"}, quants...)...)
	for _, dev := range devNames {
		tp.AddField(displayDevice(dev))
		for _, q := range quants {
			if v, ok := best[cellKey{device: dev, quant: q}]; ok {
				tp.AddField(formatFloat(v))
			} else {
				tp.AddField("-")
			}
		}
		tp.EndRow()
	}
	if err := tp.Render(); err != nil {
		return err
	}
	return llmAccuracy(ios, resp.Summary.Accuracy)
}

// quantOrder returns quant types in the order the summary presents them
// (map keys sorted for stability, since Go maps are unordered).
func quantOrder(quants map[string]gen.LlmQuantAggregates) []string {
	out := make([]string, 0, len(quants))
	for q := range quants {
		out = append(out, q)
	}
	sort.Strings(out)
	return out
}

// llmDeviceKey groups by marketing_name falling back to name.
func llmDeviceKey(d gen.LlmReportDevice) string {
	if d.MarketingName != nil && *d.MarketingName != "" {
		return *d.MarketingName
	}
	if d.Name != nil && *d.Name != "" {
		return *d.Name
	}
	return ""
}

// llmAccuracy prints the per-dataset accuracy section from the summary.
func llmAccuracy(ios *iostreams.IOStreams, entries []gen.LlmAccuracyEntry) error {
	if len(entries) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintln(&b, "\nAccuracy:")
	sorted := append([]gen.LlmAccuracyEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Dataset != sorted[j].Dataset {
			return sorted[i].Dataset < sorted[j].Dataset
		}
		return sorted[i].QuantType < sorted[j].QuantType
	})
	for _, e := range sorted {
		fmt.Fprintf(&b, "  %-12s %-10s %s\n",
			orDash(e.Dataset), orDash(e.QuantType), formatFloat(e.Score))
	}
	_, err := fmt.Fprint(ios.Out, b.String())
	return err
}
