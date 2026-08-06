package tableprinter_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func yellow(s string) string { return "\x1b[33m" + s + "\x1b[0m" }

// ttyStreams builds TTY-mode streams with color forced off so goldens are
// stable regardless of the host's TERM/NO_COLOR environment.
func ttyStreams(t *testing.T, width int) (*iostreams.IOStreams, *strings.Builder) {
	t.Helper()
	ios, out := colorTTYStreams(t, width)
	ios.SetNoColor(true)
	return ios, out
}

// colorTTYStreams builds TTY-mode streams with color forced on. iostreams.Test
// pins the encoding, so rule glyphs are stable across hosts.
func colorTTYStreams(t *testing.T, width int) (*iostreams.IOStreams, *strings.Builder) {
	t.Helper()
	t.Setenv("CLICOLOR_FORCE", "1")
	ios, _, _, _ := iostreams.Test()
	out := &strings.Builder{}
	ios.Out = out
	ios.SetStdoutTTY(true)
	ios.SetTerminalWidth(width)
	return ios, out
}

// assertFitsWidth is the invariant the whole TTY layout exists to hold: no
// rendered line may exceed the terminal, because a terminal wraps the overflow
// and the alignment the table provides is destroyed.
func assertFitsWidth(t *testing.T, rendered string, width int) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		assert.LessOrEqual(t, text.DisplayWidth(line), width,
			"line wider than the terminal will wrap: %q", line)
	}
}

func TestTTYRendersPaddedColumnsWithHeaders(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	tp := tableprinter.New(ios)
	tp.HeaderRow("repo", "visibility", "type")
	tp.AddField("zetic/whisper-tiny")
	tp.AddField("private")
	tp.AddField("general")
	tp.EndRow()
	tp.AddField("acme/detr")
	tp.AddField("public")
	tp.AddField("llm")
	tp.EndRow()
	require.NoError(t, tp.Render())

	want := "REPO                VISIBILITY  TYPE\n" +
		"──────────────────  ──────────  ───────\n" +
		"zetic/whisper-tiny  private     general\n" +
		"acme/detr           public      llm\n"
	assert.Equal(t, want, out.String())
}

// The rule is what carries the header/data boundary when the dimmed header
// cannot: NO_COLOR, TERM=dumb, or a pale theme.
func TestTTYHeaderRuleRenderedWithoutColor(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	tp := tableprinter.New(ios)
	tp.HeaderRow("repo")
	tp.AddField("zetic/whisper")
	tp.EndRow()
	require.NoError(t, tp.Render())

	lines := strings.Split(out.String(), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	assert.Equal(t, "REPO", lines[0], "the last column carries no trailing padding")
	assert.Equal(t, strings.Repeat("─", 13), lines[1],
		"the rule spans the column, so a reader sees the boundary without color")
}

func TestTTYHeaderRuleFallsBackToASCII(t *testing.T) {
	ios, out := ttyStreams(t, 80)
	ios.SetUnicode(false)

	tp := tableprinter.New(ios)
	tp.HeaderRow("repo")
	tp.AddField("zetic/whisper")
	tp.EndRow()
	require.NoError(t, tp.Render())

	assert.Contains(t, out.String(), strings.Repeat("-", 13),
		"a terminal that cannot encode U+2500 must not get a rule of replacement glyphs")
}

func TestTTYShrinksWidestColumnToFitTerminalWidth(t *testing.T) {
	ios, out := ttyStreams(t, 30)

	tp := tableprinter.New(ios)
	tp.AddField("zetic/whisper-tiny")
	tp.AddField("a description that is far too long to fit")
	tp.EndRow()
	require.NoError(t, tp.Render())

	// 18 + 41 of content cannot fit 30 columns. The loss is taken from whichever
	// column is widest at each step, so the 41-wide prose gives up 27 columns
	// before the 18-wide name gives up its first.
	assert.Equal(t, "zetic/whisp...  a descripti...\n", out.String())
	assertFitsWidth(t, out.String(), 30)
}

// The defect this rule replaced: widths were sized to content and only the LAST
// column could shrink, so a wide leading column pushed every row past the
// terminal and the terminal wrapped it. This is the shape `melange report view`
// produces — one wide device column crossed with several narrow metric columns.
func TestTTYWideLeadingColumnNeverOverflows(t *testing.T) {
	ios, out := ttyStreams(t, 40)

	tp := tableprinter.New(ios)
	tp.HeaderRow("device", "cpu/fp32", "gpu/fp16", "npu/fp16")
	tp.AddField("Galaxy S24 Ultra (Snapdragon 8 Gen 3)")
	tp.AddField("12.40")
	tp.AddField("8.10")
	tp.AddField("3.20")
	tp.EndRow()
	require.NoError(t, tp.Render())

	assertFitsWidth(t, out.String(), 40)
	assert.Contains(t, out.String(), "12.40", "narrow metric columns keep their values intact")
	assert.Contains(t, out.String(), "3.20")
}

func TestTTYWithTruncateFalseKeepsFullValueAndShrinksNeighbors(t *testing.T) {
	ios, out := ttyStreams(t, 20)

	tp := tableprinter.New(ios)
	tp.AddField("zetic/whisper")
	tp.AddField("never-truncate-me-even-if-long", tableprinter.WithTruncate(false))
	tp.EndRow()
	require.NoError(t, tp.Render())

	// The pinned 30-wide column alone exceeds the 20-column terminal, so no
	// amount of clipping can prevent a wrap. Squeezing the neighbor would lose
	// content and wrap anyway, so both values stay whole.
	assert.Equal(t, "zetic/whisper  never-truncate-me-even-if-long\n", out.String())
}

// When a fit IS achievable, the shrinkable columns do give way.
func TestTTYShrinksAroundAPinnedColumn(t *testing.T) {
	ios, out := ttyStreams(t, 40)

	tp := tableprinter.New(ios)
	tp.AddField("zetic/whisper-tiny-with-a-long-name")
	tp.AddField("/models/whisper.tflite", tableprinter.WithTruncate(false))
	tp.EndRow()
	require.NoError(t, tp.Render())

	// 40 - 2 separator columns - 22 pinned leaves 16 for the name.
	assert.Equal(t, "zetic/whisper...  /models/whisper.tflite\n", out.String())
	assertFitsWidth(t, out.String(), 40)
}

// Padding computed in runes drifts the moment a cell holds CJK text, which
// server-supplied device marketing names routinely do.
func TestTTYAlignsWideCharactersByDisplayWidth(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	tp := tableprinter.New(ios)
	tp.AddField("갤럭시")
	tp.AddField("npu")
	tp.EndRow()
	tp.AddField("Pixel 9")
	tp.AddField("gpu")
	tp.EndRow()
	require.NoError(t, tp.Render())

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	// The second column must begin at the same terminal column in both rows.
	assert.Equal(t,
		text.DisplayWidth(lines[0][:strings.Index(lines[0], "npu")]),
		text.DisplayWidth(lines[1][:strings.Index(lines[1], "gpu")]),
		"the wide-character row must not shift the next column")
	for _, line := range lines {
		assert.Equal(t, text.DisplayWidth(lines[0]), text.DisplayWidth(line),
			"both rows must occupy the same number of terminal columns: %q", line)
	}
}

func TestTTYHeadingPrintedAboveTable(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	tp := tableprinter.New(ios)
	tp.Heading("Accuracy:")
	tp.HeaderRow("dataset")
	tp.AddField("mmlu")
	tp.EndRow()
	require.NoError(t, tp.Render())

	assert.True(t, strings.HasPrefix(out.String(), "\nAccuracy:\nDATASET\n"),
		"the heading names the table it sits above, got %q", out.String())
}

func TestNonTTYHeadingOmitted(t *testing.T) {
	ios, _, out, _ := iostreams.Test()

	tp := tableprinter.New(ios)
	tp.Heading("Accuracy:")
	tp.AddField("mmlu")
	tp.EndRow()
	require.NoError(t, tp.Render())

	assert.Equal(t, "mmlu\n", out.String(),
		"the machine contract carries data rows only")
}

func TestTTYCaptionPrintedDimmedAfterTable(t *testing.T) {
	ios, out := ttyStreams(t, 80)

	tp := tableprinter.New(ios)
	tp.HeaderRow("repo")
	tp.AddField("zetic/whisper")
	tp.EndRow()
	tp.Caption("1 repository")
	require.NoError(t, tp.Render())

	assert.True(t, strings.HasSuffix(out.String(), "\n1 repository\n"),
		"the count belongs under the table, got %q", out.String())
}

func TestNonTTYCaptionOmitted(t *testing.T) {
	ios, _, out, _ := iostreams.Test()

	tp := tableprinter.New(ios)
	tp.AddField("zetic/whisper")
	tp.EndRow()
	tp.Caption("1 repository")
	require.NoError(t, tp.Render())

	assert.Equal(t, "zetic/whisper\n", out.String(),
		"the machine contract carries data rows only")
}

// --format is the one switch that decides layout, so a forced table must render
// as a table even though stdout is not a terminal, and a forced TSV must render
// the machine contract even though it is.
func TestFormatOverridesTTYDetection(t *testing.T) {
	t.Run("table forced off a TTY", func(t *testing.T) {
		ios, _, out, _ := iostreams.Test()
		ios.SetTerminalWidth(80)
		ios.SetFormat(iostreams.FormatTable)

		tp := tableprinter.New(ios)
		tp.HeaderRow("repo")
		tp.AddField("zetic/whisper")
		tp.EndRow()
		require.NoError(t, tp.Render())

		assert.Contains(t, out.String(), "REPO")
		assert.NotContains(t, out.String(), "\x1b[",
			"forcing a table into a pipe must not inject ANSI")
	})

	t.Run("tsv forced on a TTY", func(t *testing.T) {
		ios, out := ttyStreams(t, 80)
		ios.SetFormat(iostreams.FormatTSV)

		tp := tableprinter.New(ios)
		tp.HeaderRow("repo", "visibility")
		tp.AddField("zetic/whisper")
		tp.AddField("private")
		tp.EndRow()
		require.NoError(t, tp.Render())

		assert.Equal(t, "zetic/whisper\tprivate\n", out.String())
	})
}

func TestTTYColorAppliedAfterPaddingMath(t *testing.T) {
	ios, out := colorTTYStreams(t, 80)

	tp := tableprinter.New(ios)
	tp.AddField("private", tableprinter.WithColor(yellow))
	tp.AddField("general")
	tp.EndRow()
	require.NoError(t, tp.Render())

	// Padding is computed from the plain width, not the ANSI-wrapped width.
	assert.Equal(t, "\x1b[33mprivate\x1b[0m  general\n", out.String())
}

func TestTTYHeadersDimmedWhenColorEnabled(t *testing.T) {
	ios, out := colorTTYStreams(t, 80)

	tp := tableprinter.New(ios)
	tp.HeaderRow("repo")
	tp.AddField("zetic/whisper")
	tp.EndRow()
	require.NoError(t, tp.Render())

	assert.Contains(t, out.String(), "\x1b[2mREPO\x1b[0m")
}

func TestNonTTYTabSeparatedNoHeadersNoTruncationNoColor(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	ios.SetTerminalWidth(10) // must be ignored in non-TTY mode

	tp := tableprinter.New(ios)
	tp.HeaderRow("repo", "visibility")
	tp.AddField("zetic/whisper-tiny-but-quite-long")
	tp.AddField("private", tableprinter.WithColor(yellow))
	tp.EndRow()
	require.NoError(t, tp.Render())

	assert.Equal(t, "zetic/whisper-tiny-but-quite-long\tprivate\n", out.String())
}

func TestNonTTYEscapesControlCharactersWithinCells(t *testing.T) {
	ios, _, out, _ := iostreams.Test()

	tp := tableprinter.New(ios)
	tp.AddField("line1\nline2")
	tp.AddField("tab\tcarriage\rslash\\")
	tp.EndRow()
	require.NoError(t, tp.Render())

	assert.Equal(t, "line1\\nline2\ttab\\tcarriage\\rslash\\\\\n", out.String())
	assert.Len(t, strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n"), 1,
		"one logical row must always occupy one physical line")
}

func TestTableNeutralizesServerControlledOSC52(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	tp := tableprinter.New(ios)
	tp.AddField("safe\x1b]52;c;Y2xpcGJvYXJk\a text")
	tp.EndRow()
	require.NoError(t, tp.Render())

	assert.Equal(t, "safe text\n", out.String())

	ios, _, out, _ = iostreams.Test()
	tp = tableprinter.New(ios)
	tp.AddField("safe\x1b]52;c;Y2xpcGJvYXJk\x1b\\ text")
	tp.EndRow()
	require.NoError(t, tp.Render())
	assert.Equal(t, "safe text\n", out.String(), "OSC terminated by ST leaves no escape residue")
}

func TestRenderEmptyTableWritesNothing(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	tp := tableprinter.New(ios)
	require.NoError(t, tp.Render())
	assert.Empty(t, out.String())
}
