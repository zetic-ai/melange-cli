package tableprinter_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
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

// colorTTYStreams builds TTY-mode streams with color forced on.
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
		"zetic/whisper-tiny  private     general\n" +
		"acme/detr           public      llm\n"
	assert.Equal(t, want, out.String())
}

func TestTTYTruncatesLastColumnToTerminalWidth(t *testing.T) {
	ios, out := ttyStreams(t, 30)

	tp := tableprinter.New(ios)
	tp.AddField("zetic/whisper-tiny")
	tp.AddField("a description that is far too long to fit")
	tp.EndRow()
	require.NoError(t, tp.Render())

	// 18 (col1) + 2 (sep) leaves 10 columns for the last field.
	want := "zetic/whisper-tiny  a descr...\n"
	assert.Equal(t, want, out.String())
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		assert.LessOrEqual(t, len([]rune(line)), 30)
	}
}

func TestTTYWithTruncateFalseKeepsFullValue(t *testing.T) {
	ios, out := ttyStreams(t, 20)

	tp := tableprinter.New(ios)
	tp.AddField("zetic/whisper")
	tp.AddField("never-truncate-me-even-if-long", tableprinter.WithTruncate(false))
	tp.EndRow()
	require.NoError(t, tp.Render())

	assert.Contains(t, out.String(), "never-truncate-me-even-if-long")
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
