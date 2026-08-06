package tableprinter

import (
	"fmt"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// gutter separates a field label from its value.
const gutter = "  "

// Fields renders one record as a title followed by aligned "Label  value"
// lines and free-form paragraphs. It is the detail-view counterpart to
// TablePrinter: a single object is not a list, and laying one out as a
// one-row table pushes every value off the right edge.
//
// Fields is TTY-only by construction — callers keep their own tab-separated
// branch for the machine contract, which must stay byte-stable. Label widths
// are computed rather than hardcoded, so adding a longer label cannot silently
// misalign the block.
type Fields struct {
	ios          *iostreams.IOStreams
	colorEnabled bool

	title  string
	blocks []block
}

// block is either a group of aligned label/value rows or a paragraph of text.
type block struct {
	rows      [][2]string
	paragraph string
}

// NewFields builds a Fields bound to ios.Out.
func NewFields(ios *iostreams.IOStreams) *Fields {
	return &Fields{ios: ios, colorEnabled: ios.ColorEnabled()}
}

// Title sets the heading printed above the fields.
func (p *Fields) Title(s string) { p.title = s }

// Add appends a label/value row, skipping it when the value is empty: a detail
// view reads better without a column of blanks for data the object never had.
//
// The value is sanitized to a single line here rather than at render time,
// because the rendered block also carries our own color escapes — sanitizing
// the assembled output would strip those too. A field value that arrived with
// an embedded newline would otherwise break the alignment of everything after
// it.
func (p *Fields) Add(label, value string) {
	value = text.SanitizeTerminalInline(value)
	if value == "" {
		return
	}
	if n := len(p.blocks); n > 0 && p.blocks[n-1].paragraph == "" {
		p.blocks[n-1].rows = append(p.blocks[n-1].rows, [2]string{label, value})
		return
	}
	p.blocks = append(p.blocks, block{rows: [][2]string{{label, value}}})
}

// Paragraph appends a free-form block, separated from its neighbors by a blank
// line. Newlines are preserved; other terminal controls are removed.
func (p *Fields) Paragraph(s string) {
	s = text.SanitizeTerminal(s)
	if strings.TrimSpace(s) == "" {
		return
	}
	p.blocks = append(p.blocks, block{paragraph: s})
}

// Render writes the record to the output stream.
func (p *Fields) Render() error {
	var b strings.Builder
	if p.title != "" {
		b.WriteString(text.SanitizeTerminalInline(p.title))
		b.WriteString("\n")
	}
	for i, blk := range p.blocks {
		if i > 0 || p.title != "" {
			b.WriteString("\n")
		}
		if blk.paragraph != "" {
			b.WriteString(strings.TrimRight(blk.paragraph, "\n"))
			b.WriteString("\n")
			continue
		}
		p.writeRows(&b, blk.rows)
	}
	_, err := fmt.Fprint(p.ios.Out, b.String())
	return err
}

func (p *Fields) writeRows(b *strings.Builder, rows [][2]string) {
	labelWidth := 0
	for _, r := range rows {
		if w := text.DisplayWidth(r[0]) + 1; w > labelWidth { // +1 for the colon
			labelWidth = w
		}
	}
	dim := p.ios.ColorScheme().Dim
	for _, r := range rows {
		label := r[0] + ":"
		pad := strings.Repeat(" ", max(labelWidth-text.DisplayWidth(label), 0))
		if p.colorEnabled {
			label = dim(label)
		}
		b.WriteString(label)
		b.WriteString(pad)
		b.WriteString(gutter)
		b.WriteString(r[1])
		b.WriteString("\n")
	}
}
