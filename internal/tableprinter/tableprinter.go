// Package tableprinter renders a command's result for a reader or for a
// machine. It holds the two layout primitives melange commands use:
//
//   - TablePrinter (this file) for lists: a padded, width-fitted table with a
//     ruled header for humans, or stable tab-separated lines for machines.
//   - Fields (fields.go) for one record: aligned "Label  value" blocks. A
//     single object laid out as a one-row table pushes every value off the
//     right edge, so detail views need their own shape. It is human-only —
//     callers keep their own tab-separated branch.
//
// Which layout a caller gets follows IOStreams.HumanOutput (a terminal, or
// --format table), not TTY-ness directly. Human output may evolve; the
// tab-separated form is a contract (no headers, no rule, no truncation, no
// color, no caption).
package tableprinter

import (
	"fmt"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/text"
)

const (
	// colSep separates columns in TTY mode.
	colSep = "  "
	// minColWidth is the narrowest a column may be squeezed to: enough for the
	// ellipsis, so a shrunken cell still reads as truncated rather than as a
	// stray letter. Measured in terminal columns, like every width here.
	minColWidth = 3
)

type field struct {
	text     string
	color    func(string) string
	truncate bool
}

// FieldOption customizes a single field.
type FieldOption func(*field)

// WithColor colors the field in TTY mode when color is enabled. Padding is
// computed from the plain text, so ANSI sequences never skew alignment.
func WithColor(fn func(string) string) FieldOption {
	return func(f *field) { f.color = fn }
}

// WithTruncate(false) opts the field out of terminal-width truncation.
func WithTruncate(v bool) FieldOption {
	return func(f *field) { f.truncate = v }
}

// TablePrinter accumulates rows and renders them on Render.
type TablePrinter struct {
	ios          *iostreams.IOStreams
	human        bool
	maxWidth     int
	colorEnabled bool
	ruleChar     string

	hasHeader bool
	heading   string
	caption   string
	rows      [][]field
	current   []field
}

// New builds a TablePrinter bound to ios.Out, keyed off whether the caller
// wants human output (a TTY, or --format table).
func New(ios *iostreams.IOStreams) *TablePrinter {
	return &TablePrinter{
		ios:          ios,
		human:        ios.HumanOutput(),
		maxWidth:     ios.TerminalWidth(),
		colorEnabled: ios.ColorEnabled(),
		ruleChar:     ios.RuleChar(),
	}
}

// HeaderRow sets the column headers. In TTY mode they render uppercased (and
// dimmed when color is enabled) above a rule that separates them from the data;
// in non-TTY mode headers are omitted entirely. The header row is prepended to
// the accumulated rows, so it may be called at any point before Render,
// regardless of AddField/EndRow ordering.
func (t *TablePrinter) HeaderRow(cols ...string) {
	if !t.human || len(cols) == 0 {
		return
	}
	dim := t.ios.ColorScheme().Dim
	header := make([]field, len(cols))
	for i, c := range cols {
		header[i] = field{text: strings.ToUpper(c), color: dim, truncate: true}
	}
	t.rows = append([][]field{header}, t.rows...)
	t.hasHeader = true
}

// Heading sets a label printed above the table, preceded by a blank line, in
// human mode only. Reports stack several tables in one response and each needs
// naming; without this the callers hand-print the label and the table primitive
// owns only half of its own layout.
func (t *TablePrinter) Heading(s string) {
	t.heading = s
}

// Caption sets a one-line summary printed under the table, dimmed, in TTY mode
// only — it tells a reader how much they are looking at without disturbing the
// tab-separated machine contract.
func (t *TablePrinter) Caption(s string) {
	t.caption = s
}

// AddField appends a field to the current row.
func (t *TablePrinter) AddField(s string, opts ...FieldOption) {
	f := field{text: s, truncate: true}
	for _, opt := range opts {
		opt(&f)
	}
	t.current = append(t.current, f)
}

// EndRow finishes the current row.
func (t *TablePrinter) EndRow() {
	t.rows = append(t.rows, t.current)
	t.current = nil
}

// Render writes all rows to the output stream.
func (t *TablePrinter) Render() error {
	if len(t.current) > 0 {
		t.EndRow()
	}
	if len(t.rows) == 0 {
		return nil
	}
	if !t.human {
		return t.renderTSV()
	}
	return t.renderTTY()
}

// renderTSV emits the machine contract: one row per line, tab-separated raw
// values — no headers, no truncation, no color, no caption.
func (t *TablePrinter) renderTSV() error {
	for _, row := range t.rows {
		cells := make([]string, len(row))
		for i, f := range row {
			cells[i] = text.EscapeTSVCell(f.text)
		}
		if _, err := fmt.Fprintln(t.ios.Out, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return nil
}

// renderTTY emits padded columns sized to fit the terminal, a rule under the
// header, and the caption.
func (t *TablePrinter) renderTTY() error {
	if t.heading != "" {
		heading := text.SanitizeTerminalInline(t.heading)
		if _, err := fmt.Fprintf(t.ios.Out, "\n%s\n", heading); err != nil {
			return err
		}
	}
	widths := t.columnWidths()
	for i, row := range t.rows {
		if _, err := fmt.Fprintln(t.ios.Out, t.renderRow(row, widths)); err != nil {
			return err
		}
		if i == 0 && t.hasHeader {
			if _, err := fmt.Fprintln(t.ios.Out, t.renderRule(widths)); err != nil {
				return err
			}
		}
	}
	if t.caption == "" {
		return nil
	}
	caption := text.SanitizeTerminalInline(t.caption)
	if t.colorEnabled {
		caption = t.ios.ColorScheme().Dim(caption)
	}
	_, err := fmt.Fprintf(t.ios.Out, "\n%s\n", caption)
	return err
}

func (t *TablePrinter) renderRow(row []field, widths []int) string {
	var b strings.Builder
	for i, f := range row {
		if i > 0 {
			b.WriteString(colSep)
		}
		cell := text.SanitizeTerminalInline(f.text)
		if f.truncate && i < len(widths) {
			cell = text.Truncate(widths[i], cell)
		}
		rendered := cell
		if f.color != nil && t.colorEnabled {
			rendered = f.color(cell)
		}
		b.WriteString(rendered)
		if i < len(row)-1 { // last column carries no trailing padding
			b.WriteString(strings.Repeat(" ", max(widths[i]-text.DisplayWidth(cell), 0)))
		}
	}
	return b.String()
}

// renderRule underlines the header. It carries the table's structure without
// relying on color, which the dimmed header alone cannot do under NO_COLOR, a
// "dumb" TERM, or a low-contrast theme.
func (t *TablePrinter) renderRule(widths []int) string {
	segments := make([]string, len(widths))
	for i, w := range widths {
		segments[i] = strings.Repeat(t.ruleChar, w)
	}
	rule := strings.Join(segments, colSep)
	if t.colorEnabled {
		rule = t.ios.ColorScheme().Dim(rule)
	}
	return rule
}

// columnWidths sizes every column to its widest cell, then shrinks the table to
// the terminal width by repeatedly taking a column from whichever truncatable
// column is currently widest.
//
// Shrinking the widest column spends the loss where the content is most
// compressible and leaves narrow identifier columns intact. Sizing to content
// and shrinking only the LAST column — the older rule — let any wide leading
// column (a long device marketing name, a report's device column crossed with N
// accelerator/precision columns) push the row past the terminal width, where it
// wrapped and destroyed the alignment the table exists to provide.
func (t *TablePrinter) columnWidths() []int {
	numCols := 0
	for _, row := range t.rows {
		if len(row) > numCols {
			numCols = len(row)
		}
	}
	if numCols == 0 {
		return nil
	}

	widths := make([]int, numCols)
	shrinkable := make([]bool, numCols)
	for i := range shrinkable {
		shrinkable[i] = true
	}
	for _, row := range t.rows {
		for i, f := range row {
			if w := text.DisplayWidth(text.SanitizeTerminalInline(f.text)); w > widths[i] {
				widths[i] = w
			}
			// A single opted-out field pins the whole column: WithTruncate(false)
			// marks values that are wrong when clipped, such as file paths.
			if !f.truncate {
				shrinkable[i] = false
			}
		}
	}

	available := t.maxWidth - len(colSep)*(numCols-1)
	// Shrinking is only worth its cost when it can actually achieve a fit. When
	// pinned columns alone already exceed the terminal, the rows will wrap
	// whatever we do, so clipping the shrinkable columns would destroy content
	// and still wrap. Leave the values whole in that case.
	if floorTotal(widths, shrinkable) > available {
		return widths
	}
	for total(widths) > available {
		i := widestShrinkable(widths, shrinkable)
		if i < 0 {
			break
		}
		widths[i]--
	}
	return widths
}

func total(widths []int) int {
	sum := 0
	for _, w := range widths {
		sum += w
	}
	return sum
}

// floorTotal is the narrowest the table can become: shrinkable columns squeezed
// to minColWidth, pinned columns at their full content width.
func floorTotal(widths []int, shrinkable []bool) int {
	sum := 0
	for i, w := range widths {
		if shrinkable[i] {
			sum += min(w, minColWidth)
			continue
		}
		sum += w
	}
	return sum
}

// widestShrinkable returns the index of the widest column that may still give up
// a column, or -1 when every column is at its floor. Ties go to the leftmost so
// shrinking is deterministic.
func widestShrinkable(widths []int, shrinkable []bool) int {
	best := -1
	for i, w := range widths {
		if !shrinkable[i] || w <= minColWidth {
			continue
		}
		if best < 0 || w > widths[best] {
			best = i
		}
	}
	return best
}
