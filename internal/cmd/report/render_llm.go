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
	"github.com/zetic-ai/melange-cli/internal/text"
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
		var device gen.ReportDevice
		if r.Device != nil {
			device = *r.Device
		}
		tp.AddField(deref(device.MarketingName))
		tp.AddField(deref(device.Name))
		tp.AddField(deref(device.Soc))
		tp.AddField(deref(device.Os))
		tp.AddField(deref(r.ApType))
		tp.AddField(deref(r.Target))
		tp.AddField(deref(r.QuantType))
		tp.AddField(deref(r.Dataset))
		tp.AddField(strconv.Itoa(r.Run))
		tp.AddField(string(r.Metric))
		tp.AddField(formatRawFloat(r.Value))
		tp.AddField(string(r.Unit))
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
		quant := deref(r.QuantType)
		if r.Metric != metricTps || quant == "" || r.Device == nil {
			continue
		}
		dev := llmDeviceKey(*r.Device)
		devices[dev] = true
		k := cellKey{device: dev, quant: quant}
		if cur, ok := best[k]; !ok || r.Value > cur {
			best[k] = r.Value
		}
	}

	// Union summary quants with quant types seen in records so populated cells
	// remain visible even when the summary carries no entry for that quant.
	recordQuants := map[string]bool{}
	for k := range best {
		recordQuants[k.quant] = true
	}
	quants := quantOrder(resp.Summary.Quants, recordQuants)
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

// quantOrder returns the union of summary quant types and quant types seen
// in records, sorted for stability (Go maps are unordered).
func quantOrder(quants map[string]gen.LlmQuantAggregates, recordQuants map[string]bool) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(quants)+len(recordQuants))
	for q := range quants {
		if !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	for q := range recordQuants {
		if !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	sort.Strings(out)
	return out
}

// llmDeviceKey groups by marketing_name falling back to name.
func llmDeviceKey(d gen.ReportDevice) string {
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
		leftDataset, rightDataset := deref(sorted[i].Dataset), deref(sorted[j].Dataset)
		if leftDataset != rightDataset {
			return leftDataset < rightDataset
		}
		return deref(sorted[i].QuantType) < deref(sorted[j].QuantType)
	})
	for _, e := range sorted {
		fmt.Fprintf(&b, "  %-12s %-10s %s\n",
			orDash(deref(e.Dataset)), orDash(deref(e.QuantType)), formatFloat(e.Score))
	}
	_, err := fmt.Fprint(ios.Out, text.SanitizeTerminal(b.String()))
	return err
}
