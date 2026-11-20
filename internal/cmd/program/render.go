package program

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// The render views decode the response themselves rather than through the
// generated types: those hold metric values as float32 and their keyed
// values in Go maps, which would rewrite numbers and lose key order. Here a
// value is the API's number text, and keyed values keep their order.

type page struct {
	Program   string                 `json:"program"`
	UpdatedAt *string                `json:"updated_at"`
	Metrics   map[string][]metricDef `json:"metrics"`
	Targets   []target               `json:"targets"`
	Models    []model                `json:"models"`
}

type metricDef struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Category    string  `json:"category"`
	Unit        *string `json:"unit"`
	KeyedBy     *string `json:"keyed_by"`
	Summary     bool    `json:"summary"`
	Approximate bool    `json:"approximate"`
}

type target struct {
	Soc    string  `json:"soc"`
	Name   string  `json:"name"`
	Npu    *string `json:"npu"`
	Device struct {
		Name  string  `json:"name"`
		Model *string `json:"model"`
	} `json:"device"`
	HasResults bool `json:"has_results"`
}

type model struct {
	Slug     string    `json:"slug"`
	Task     string    `json:"task"`
	Name     string    `json:"name"`
	Vendor   *string   `json:"vendor"`
	Variants []variant `json:"variants"`
	Results  []result  `json:"results"`
}

type variant struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type result struct {
	Soc        string   `json:"soc"`
	MeasuredOn string   `json:"measured_on"`
	AsOf       string   `json:"as_of"`
	Highlights []string `json:"highlights"`
	Builds     []build  `json:"builds"`
}

type build struct {
	Key        string       `json:"key"`
	Label      string       `json:"label"`
	Runtime    string       `json:"runtime"`
	Status     string       `json:"status"`
	StatusNote *string      `json:"status_note"`
	Variant    *string      `json:"variant"`
	Modes      []string     `json:"modes"`
	Speed      metricValues `json:"speed"`
	Accuracy   metricValues `json:"accuracy"`
}

// metricValues is a build's speed or accuracy object, in API order.
type metricValues []metricEntry

type metricEntry struct {
	key   string
	value metricValue
}

// metricValue is a plain number, or numbers keyed by prompt length or test
// set.
type metricValue struct {
	number json.Number
	keyed  []keyedNumber
}

type keyedNumber struct {
	key   string
	value json.Number
}

const statusMeasured = "measured"

func decodePage(body []byte) (*page, error) {
	var p page
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("decoding program benchmarks: %w", err)
	}
	return &p, nil
}

// UnmarshalJSON keeps the metrics in API order.
func (m *metricValues) UnmarshalJSON(raw []byte) error {
	*m = nil
	return eachMember(raw, func(key string, value json.RawMessage) error {
		parsed, err := parseMetricValue(value)
		if err != nil {
			return fmt.Errorf("metric %s: %w", key, err)
		}
		*m = append(*m, metricEntry{key: key, value: parsed})
		return nil
	})
}

func parseMetricValue(raw json.RawMessage) (metricValue, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var keyed []keyedNumber
		err := eachMember(trimmed, func(key string, value json.RawMessage) error {
			var n json.Number
			if err := json.Unmarshal(value, &n); err != nil {
				return err
			}
			keyed = append(keyed, keyedNumber{key: key, value: n})
			return nil
		})
		return metricValue{keyed: keyed}, err
	}
	var n json.Number
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return metricValue{}, err
	}
	return metricValue{number: n}, nil
}

// eachMember walks a JSON object's members in order; null is no members.
func eachMember(raw []byte, fn func(key string, value json.RawMessage) error) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return errors.New("expected a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("expected an object key")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		if err := fn(key, value); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

// metric returns a build's value for key from speed or accuracy.
func (b build) metric(key string) (metricValue, bool) {
	for _, entries := range []metricValues{b.Speed, b.Accuracy} {
		for _, e := range entries {
			if e.key == key {
				return e.value, true
			}
		}
	}
	return metricValue{}, false
}

// metricOrder returns the task's vocabulary followed by any metric a build
// carries that the vocabulary does not list (in build order), so nothing the
// API sent is dropped.
func metricOrder(defs []metricDef, builds []build) []metricDef {
	out := append([]metricDef(nil), defs...)
	known := map[string]bool{}
	for _, d := range defs {
		known[d.Key] = true
	}
	for _, b := range builds {
		for _, entries := range []metricValues{b.Speed, b.Accuracy} {
			for _, e := range entries {
				if !known[e.key] {
					known[e.key] = true
					out = append(out, metricDef{Key: e.key, Label: e.key})
				}
			}
		}
	}
	return out
}

// orderedKeys sorts prompt-length keys numerically (128 before 1024) and
// keeps every other key set in API order.
func orderedKeys(d metricDef, keyed []keyedNumber) []keyedNumber {
	out := append([]keyedNumber(nil), keyed...)
	if d.KeyedBy == nil || *d.KeyedBy != "prompt_tokens" {
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, lerr := strconv.Atoi(out[i].key)
		right, rerr := strconv.Atoi(out[j].key)
		if lerr != nil || rerr != nil {
			return false
		}
		return left < right
	})
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---------------------------------------------------------------------------
// tab-separated machine output
// ---------------------------------------------------------------------------

// renderTSV prints one metric value per line: soc, model, task, variant,
// build, runtime, status, modes, metric, key, value, unit.
func renderTSV(ios *iostreams.IOStreams, p *page) error {
	tp := tableprinter.New(ios)
	for _, m := range p.Models {
		for _, r := range m.Results {
			defs := metricOrder(p.Metrics[m.Task], r.Builds)
			for _, b := range r.Builds {
				row := func(metric, key, value, unit string) {
					for _, cell := range []string{
						r.Soc, m.Slug, m.Task, deref(b.Variant), b.Key, b.Runtime, b.Status,
						strings.Join(b.Modes, ","), metric, key, value, unit,
					} {
						tp.AddField(cell)
					}
					tp.EndRow()
				}
				wrote := false
				for _, d := range defs {
					v, ok := b.metric(d.Key)
					if !ok {
						continue
					}
					wrote = true
					if v.keyed == nil {
						row(d.Key, "", v.number.String(), deref(d.Unit))
						continue
					}
					for _, k := range orderedKeys(d, v.keyed) {
						row(d.Key, k.key, k.value.String(), deref(d.Unit))
					}
				}
				if !wrote {
					row("", "", "", "")
				}
			}
		}
	}
	return tp.Render()
}

// ---------------------------------------------------------------------------
// terminal output
// ---------------------------------------------------------------------------

var taskLabels = map[string]string{
	"llm":                  "LLM",
	"asr":                  "ASR (speech to text)",
	"tts":                  "TTS (text to speech)",
	"image_classification": "Image classification",
}

func taskLabel(task string) string {
	if label, ok := taskLabels[task]; ok {
		return label
	}
	return task
}

// renderTTY prints, per model and SoC, the builds against the task's summary
// metrics, then the SoCs still to come and where the rest of the data is.
func renderTTY(ios *iostreams.IOStreams, p *page, cli string) error {
	out := ios.Out
	title := p.Program + " benchmarks"
	if p.UpdatedAt != nil && len(*p.UpdatedAt) >= len("2006-01-02") {
		title += ", updated " + (*p.UpdatedAt)[:len("2006-01-02")]
	}
	if _, err := fmt.Fprintln(out, text.SanitizeTerminalInline(title)); err != nil {
		return err
	}

	targets := map[string]target{}
	for _, t := range p.Targets {
		targets[t.Soc] = t
	}
	shown := 0
	for _, m := range p.Models {
		if len(m.Results) == 0 {
			continue
		}
		shown++
		parts := []string{m.Name, taskLabel(m.Task)}
		if v := deref(m.Vendor); v != "" {
			parts = append(parts, v)
		}
		if _, err := fmt.Fprintf(out, "\n%s\n",
			text.SanitizeTerminalInline(strings.Join(parts, " · "))); err != nil {
			return err
		}
		for _, r := range m.Results {
			if err := renderResult(ios, p, m, r, targets[r.Soc]); err != nil {
				return err
			}
		}
	}
	if shown == 0 {
		if _, err := fmt.Fprintln(out, "\nNo results to show."); err != nil {
			return err
		}
	}

	var pending []string
	for _, t := range p.Targets {
		if !t.HasResults {
			pending = append(pending, "  "+targetLine(t))
		}
	}
	if len(pending) > 0 {
		if _, err := fmt.Fprintf(out, "\nNo results yet:\n%s\n",
			text.SanitizeTerminal(strings.Join(pending, "\n"))); err != nil {
			return err
		}
	}
	if shown > 0 {
		_, err := fmt.Fprintf(out,
			"\nMethod, notes and extra tables: `%s program benchmarks %s --json`.\n",
			cli, text.SanitizeTerminalInline(p.Program))
		return err
	}
	return nil
}

// targetLine names a catalog SoC and the device it is measured on.
func targetLine(t target) string {
	line := t.Soc
	var about []string
	if t.Name != "" {
		about = append(about, t.Name)
	}
	if npu := deref(t.Npu); npu != "" {
		about = append(about, npu)
	}
	if len(about) > 0 {
		line += " (" + strings.Join(about, ", ") + ")"
	}
	if device := deviceLabel(t); device != "" {
		line += ", measured on " + device
	}
	return line
}

func deviceLabel(t target) string {
	if t.Device.Name == "" {
		return ""
	}
	if model := deref(t.Device.Model); model != "" {
		return t.Device.Name + " " + model
	}
	return t.Device.Name
}

// Inference modes. Auto has no build of its own: it runs the Speed build.
const (
	modeSpeed    = "speed"
	modeAccuracy = "accuracy"
)

// categoryOrder is the column order of the summary metrics; categories the
// vocabulary adds later go after these, in vocabulary order.
var categoryOrder = []string{"speed", "memory", "energy", "size", "accuracy"}

// row is one line of a result table: a mode (or, for a build that serves no
// mode, "-") and the build shown for it.
type row struct {
	variant string
	mode    string
	build   build
}

// modeRows lists, per variant, Auto / Speed / Accuracy and the build serving
// each, then that variant's builds that serve no mode (a float reference, an
// unsupported build), so every build of the result is shown at least once.
// A mode no build serves is left out.
func modeRows(m model, r result) []row {
	variants := []string{""}
	if len(m.Variants) > 0 {
		variants = variants[:0]
		for _, v := range m.Variants {
			variants = append(variants, v.Key)
		}
	}
	var rows []row
	shown := map[int]bool{}
	for _, v := range variants {
		serving := func(mode string) (int, bool) {
			for i, b := range r.Builds {
				if deref(b.Variant) == v && contains(b.Modes, mode) {
					return i, true
				}
			}
			return 0, false
		}
		if i, ok := serving(modeSpeed); ok {
			rows = append(rows, row{v, "Auto", r.Builds[i]}, row{v, "Speed", r.Builds[i]})
			shown[i] = true
		}
		if i, ok := serving(modeAccuracy); ok {
			rows = append(rows, row{v, "Accuracy", r.Builds[i]})
			shown[i] = true
		}
		rows = appendModeless(rows, r.Builds, shown, func(b build) bool { return deref(b.Variant) == v })
	}
	// Builds of a variant the model does not declare.
	return appendModeless(rows, r.Builds, shown, func(build) bool { return true })
}

// appendModeless adds the builds not shown yet that match, with mode "-"
// (or their modes as given, when none is speed or accuracy).
func appendModeless(rows []row, builds []build, shown map[int]bool, match func(build) bool) []row {
	for i, b := range builds {
		if shown[i] || !match(b) {
			continue
		}
		shown[i] = true
		mode := "-"
		if len(b.Modes) > 0 {
			mode = strings.Join(b.Modes, ", ")
		}
		rows = append(rows, row{deref(b.Variant), mode, b})
	}
	return rows
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// summaryColumns returns the task's summary metrics that some build of the
// result carries, ordered by category (speed, memory, energy, size,
// accuracy) and by vocabulary order within one. Without a vocabulary for the
// task, every metric the builds carry is a column.
func summaryColumns(defs []metricDef, builds []build) []metricDef {
	var present []metricDef
	for _, d := range metricOrder(defs, builds) {
		if !d.Summary && defs != nil {
			continue
		}
		for _, b := range builds {
			if _, ok := b.metric(d.Key); ok {
				present = append(present, d)
				break
			}
		}
	}
	rank := func(category string) int {
		for i, c := range categoryOrder {
			if c == category {
				return i
			}
		}
		return len(categoryOrder)
	}
	sort.SliceStable(present, func(i, j int) bool {
		return rank(present[i].Category) < rank(present[j].Category)
	})
	return present
}

func renderResult(ios *iostreams.IOStreams, p *page, m model, r result, t target) error {
	heading := r.Soc
	if t.Name != "" {
		heading += " " + t.Name
	}
	if r.MeasuredOn != "" {
		heading += " · measured on " + r.MeasuredOn
	}
	if r.AsOf != "" {
		heading += " · as of " + r.AsOf
	}

	columns := summaryColumns(p.Metrics[m.Task], r.Builds)
	rows := modeRows(m, r)
	variants := map[string]string{}
	for _, v := range m.Variants {
		variants[v.Key] = v.Label
	}
	showVariant := len(m.Variants) > 0
	showStatus, showAuto := false, false
	for _, rw := range rows {
		if rw.build.Status != statusMeasured {
			showStatus = true
		}
		if rw.mode == "Auto" {
			showAuto = true
		}
	}

	tp := tableprinter.New(ios)
	tp.Heading(heading)
	var header []string
	if showVariant {
		header = append(header, "variant")
	}
	header = append(header, "mode", "build")
	if showStatus {
		header = append(header, "status")
	}
	for _, d := range columns {
		header = append(header, columnHeader(d))
	}
	tp.HeaderRow(header...)

	for _, rw := range rows {
		b := rw.build
		if showVariant {
			v := rw.variant
			if l, ok := variants[v]; ok {
				v = l
			}
			tp.AddField(orDash(v))
		}
		tp.AddField(rw.mode)
		tp.AddField(b.Label)
		if showStatus {
			status := strings.ReplaceAll(b.Status, "_", " ")
			if note := deref(b.StatusNote); note != "" && b.Status != statusMeasured {
				status += ": " + note
			}
			tp.AddField(status)
		}
		for _, d := range columns {
			v, ok := b.metric(d.Key)
			if !ok {
				tp.AddField("-")
				continue
			}
			tp.AddField(formatCell(d, v))
		}
		tp.EndRow()
	}
	if showAuto {
		tp.Caption("Auto runs the Speed build.")
	}
	if err := tp.Render(); err != nil {
		return err
	}
	if len(r.Highlights) == 0 {
		return nil
	}
	lines := make([]string, len(r.Highlights))
	for i, h := range r.Highlights {
		lines[i] = "- " + text.SanitizeTerminalInline(h)
	}
	_, err := fmt.Fprintf(ios.Out, "\n%s\n", strings.Join(lines, "\n"))
	return err
}

// columnHeader is the metric label with its unit, and what keyed values are
// keyed by.
func columnHeader(d metricDef) string {
	var about []string
	if unit := deref(d.Unit); unit != "" {
		about = append(about, unit)
	}
	switch deref(d.KeyedBy) {
	case "prompt_tokens":
		about = append(about, "by prompt tokens")
	case "test_set":
		about = append(about, "by test set")
	}
	if len(about) == 0 {
		return d.Label
	}
	return d.Label + " (" + strings.Join(about, ", ") + ")"
}

func formatCell(d metricDef, v metricValue) string {
	prefix := ""
	if d.Approximate {
		prefix = "~"
	}
	if v.keyed == nil {
		return prefix + v.number.String()
	}
	parts := make([]string, 0, len(v.keyed))
	for _, k := range orderedKeys(d, v.keyed) {
		parts = append(parts, k.key+": "+prefix+k.value.String())
	}
	return strings.Join(parts, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
