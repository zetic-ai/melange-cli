// Package tableprinter renders rows of fields either as a padded, truncated
// table for terminals or as stable tab-separated lines for machine consumers.
// It is THE list-output primitive for melange commands: TTY output may evolve,
// non-TTY output is a contract (tab-separated, no headers, no truncation, no
// color).
package tableprinter

import (
	"fmt"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// colSep separates columns in TTY mode.
const colSep = "  "

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
	isTTY        bool
	maxWidth     int
	colorEnabled bool

	hasHeader bool
	rows      [][]field
	current   []field
}

// New builds a TablePrinter bound to ios.Out, keyed off stdout TTY-ness.
func New(ios *iostreams.IOStreams) *TablePrinter {
	return &TablePrinter{
		ios:          ios,
		isTTY:        ios.IsStdoutTTY(),
		maxWidth:     ios.TerminalWidth(),
		colorEnabled: ios.ColorEnabled(),
	}
}

// HeaderRow sets the column headers. In TTY mode they render uppercased (and
// dimmed when color is enabled); in non-TTY mode headers are omitted entirely.
// The header row is prepended to the accumulated rows, so it may be called at
// any point before Render, regardless of AddField/EndRow ordering.
func (t *TablePrinter) HeaderRow(cols ...string) {
	if !t.isTTY || len(cols) == 0 {
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
	if !t.isTTY {
		return t.renderTSV()
	}
	return t.renderTTY()
}

// renderTSV emits the machine contract: one row per line, tab-separated raw
// values — no headers, no truncation, no color.
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

// renderTTY emits padded columns sized to content, truncated to the terminal
// width with the last column flexible.
func (t *TablePrinter) renderTTY() error {
	widths := t.columnWidths()
	for _, row := range t.rows {
		var b strings.Builder
		for i, f := range row {
			if i > 0 {
				b.WriteString(colSep)
			}
			cell := f.text
			if f.truncate && i < len(widths) {
				cell = text.Truncate(widths[i], cell)
			}
			rendered := cell
			if f.color != nil && t.colorEnabled {
				rendered = f.color(cell)
			}
			b.WriteString(rendered)
			if i < len(row)-1 { // last column carries no trailing padding
				b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(cell))))
			}
		}
		if _, err := fmt.Fprintln(t.ios.Out, b.String()); err != nil {
			return err
		}
	}
	return nil
}

// columnWidths sizes every column to its widest content, then shrinks the
// last column so rows fit the terminal width (floor of 3 for the ellipsis).
func (t *TablePrinter) columnWidths() []int {
	numCols := 0
	for _, row := range t.rows {
		if len(row) > numCols {
			numCols = len(row)
		}
	}
	widths := make([]int, numCols)
	for _, row := range t.rows {
		for i, f := range row {
			if w := len([]rune(f.text)); w > widths[i] {
				widths[i] = w
			}
		}
	}
	if numCols == 0 {
		return widths
	}
	available := t.maxWidth - colSep2Width(numCols)
	used := 0
	for _, w := range widths[:numCols-1] {
		used += w
	}
	if last := available - used; last < widths[numCols-1] {
		widths[numCols-1] = max(last, len("..."))
	}
	return widths
}

// colSep2Width is the total width consumed by column separators.
func colSep2Width(numCols int) int {
	return len(colSep) * (numCols - 1)
}
