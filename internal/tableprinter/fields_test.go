package tableprinter_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
)

func TestFieldsAlignsLabelsToTheLongest(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	p := tableprinter.NewFields(ios)
	p.Title("zetic/whisper-tiny")
	p.Add("Visibility", "private")
	p.Add("Use case", "speech")
	p.Add("Type", "general")
	require.NoError(t, p.Render())

	want := "zetic/whisper-tiny\n" +
		"\n" +
		"Visibility:  private\n" +
		"Use case:    speech\n" +
		"Type:        general\n"
	assert.Equal(t, want, out.String())
}

func TestFieldsSkipsEmptyValues(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	p := tableprinter.NewFields(ios)
	p.Add("Visibility", "private")
	p.Add("Use case", "")
	p.Add("Type", "general")
	require.NoError(t, p.Render())

	assert.Equal(t, "Visibility:  private\nType:        general\n", out.String(),
		"a field the object never had should not leave a blank row")
}

// Adding a longer label must re-align the block rather than misalign it, which
// is what hardcoded padding widths could not guarantee.
func TestFieldsRealignsWhenALongerLabelAppears(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	p := tableprinter.NewFields(ios)
	p.Add("Type", "general")
	p.Add("A considerably longer label", "x")
	require.NoError(t, p.Render())

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, strings.Index(lines[0], "general"), strings.Index(lines[1], "x"))
}

func TestFieldsParagraphsSeparatedByBlankLines(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	p := tableprinter.NewFields(ios)
	p.Title("zetic/whisper-tiny")
	p.Add("Type", "general")
	p.Paragraph("A tiny speech model.")
	p.Add("Created", "3d ago")
	require.NoError(t, p.Render())

	want := "zetic/whisper-tiny\n" +
		"\n" +
		"Type:  general\n" +
		"\n" +
		"A tiny speech model.\n" +
		"\n" +
		"Created:  3d ago\n"
	assert.Equal(t, want, out.String())
}

func TestFieldsSkipsBlankParagraph(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	p := tableprinter.NewFields(ios)
	p.Add("Type", "general")
	p.Paragraph("   \n  ")
	require.NoError(t, p.Render())

	assert.Equal(t, "Type:  general\n", out.String())
}

func TestFieldsDimsLabelsWhenColorEnabled(t *testing.T) {
	ios, out := colorTTYStreams(t, 80)

	p := tableprinter.NewFields(ios)
	p.Add("Type", "general")
	require.NoError(t, p.Render())

	assert.Equal(t, "\x1b[2mType:\x1b[0m  general\n", out.String(),
		"padding is computed from the plain label, so ANSI cannot skew alignment")
}

func TestFieldsNeutralizesTerminalEscapesInValues(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	p := tableprinter.NewFields(ios)
	p.Add("Name", "safe\x1b]52;c;Y2xpcGJvYXJk\a text")
	require.NoError(t, p.Render())

	assert.Equal(t, "Name:  safe text\n", out.String())
}

func TestFieldsRenderEmptyWritesNothing(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	p := tableprinter.NewFields(ios)
	require.NoError(t, p.Render())

	assert.Empty(t, out.String())
}
