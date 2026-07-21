package iostreams_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

func TestTest(t *testing.T) {
	streams, in, out, errOut := iostreams.Test()
	assert.NotNil(t, streams)
	assert.NotNil(t, in)
	assert.NotNil(t, out)
	assert.NotNil(t, errOut)

	// Write to Out and verify buffer captured it
	_, err := streams.Out.Write([]byte("hello"))
	assert.NoError(t, err)
	assert.Equal(t, "hello", out.String())

	// Write to ErrOut
	_, err = streams.ErrOut.Write([]byte("err"))
	assert.NoError(t, err)
	assert.Equal(t, "err", errOut.String())

	// Write to In
	in.WriteString("input")
	buf := make([]byte, 5)
	_, err = streams.In.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, "input", string(buf))
}

func TestColorEnabled(t *testing.T) {
	tests := []struct {
		name            string
		stdoutTTY       bool
		noColor         string // NO_COLOR env value ("" means unset)
		term            string // TERM env value
		cliForcedColor  string // CLICOLOR_FORCE env value
		noColorOverride bool   // --no-color flag
		want            bool
	}{
		{
			name:      "tty, no env overrides → color enabled",
			stdoutTTY: true,
			want:      true,
		},
		{
			name:      "not a tty → no color",
			stdoutTTY: false,
			want:      false,
		},
		{
			name:      "tty + NO_COLOR set → no color",
			stdoutTTY: true,
			noColor:   "1",
			want:      false,
		},
		{
			name:      "tty + TERM=dumb → no color",
			stdoutTTY: true,
			term:      "dumb",
			want:      false,
		},
		{
			name:           "not tty but CLICOLOR_FORCE=1 → color forced",
			stdoutTTY:      false,
			cliForcedColor: "1",
			want:           true,
		},
		{
			name:           "not tty but CLICOLOR_FORCE=true → color forced (non-0)",
			stdoutTTY:      false,
			cliForcedColor: "true",
			want:           true,
		},
		{
			name:           "CLICOLOR_FORCE=0 → does not force",
			stdoutTTY:      false,
			cliForcedColor: "0",
			want:           false,
		},
		{
			name:           "CLICOLOR_FORCE=1 + NO_COLOR → CLICOLOR_FORCE wins (force overrides all)",
			stdoutTTY:      false,
			cliForcedColor: "1",
			noColor:        "1",
			want:           true,
		},
		{
			name:            "--no-color override → disabled even on tty",
			stdoutTTY:       true,
			noColorOverride: true,
			want:            false,
		},
		{
			name:            "--no-color override disables even with CLICOLOR_FORCE",
			stdoutTTY:       false,
			cliForcedColor:  "1",
			noColorOverride: true,
			want:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			streams, _, _, _ := iostreams.Test()
			streams.SetStdoutTTY(tt.stdoutTTY)

			// Simulate env vars via setters
			if tt.noColor != "" {
				t.Setenv("NO_COLOR", tt.noColor)
			}
			if tt.term != "" {
				t.Setenv("TERM", tt.term)
			}
			if tt.cliForcedColor != "" {
				t.Setenv("CLICOLOR_FORCE", tt.cliForcedColor)
			}
			if tt.noColorOverride {
				streams.SetNoColor(true)
			}

			assert.Equal(t, tt.want, streams.ColorEnabled())
		})
	}
}

func TestTerminalWidth(t *testing.T) {
	streams, _, _, _ := iostreams.Test()
	// Test streams are not a TTY → should return 80
	assert.Equal(t, 80, streams.TerminalWidth())
}

func TestTTYSetters(t *testing.T) {
	streams, _, _, _ := iostreams.Test()

	streams.SetStdinTTY(true)
	assert.True(t, streams.IsStdinTTY())

	streams.SetStdoutTTY(true)
	assert.True(t, streams.IsStdoutTTY())

	streams.SetStderrTTY(true)
	assert.True(t, streams.IsStderrTTY())
}

// Verify In is an io.Reader (compile-time check via assignment)
func TestInIsReader(t *testing.T) {
	streams, _, _, _ := iostreams.Test()
	var _ interface{ Read([]byte) (int, error) } = streams.In
	_ = streams
}

// Verify Out/ErrOut are io.Writer
func TestOutIsWriter(t *testing.T) {
	streams, _, _, _ := iostreams.Test()
	var _ interface{ Write([]byte) (int, error) } = streams.Out
	var _ interface{ Write([]byte) (int, error) } = streams.ErrOut
	_ = streams
}

func TestTestReturnsBuffers(t *testing.T) {
	_, in, out, errOut := iostreams.Test()
	assert.IsType(t, &bytes.Buffer{}, in)
	assert.IsType(t, &bytes.Buffer{}, out)
	assert.IsType(t, &bytes.Buffer{}, errOut)
}
