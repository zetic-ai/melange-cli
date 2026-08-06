// Package iostreams provides the IOStreams abstraction used throughout the CLI.
// All commands receive an *IOStreams so they can write to the right place and
// respect color/TTY settings without touching os.Stdout directly.
package iostreams

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	colorable "github.com/mattn/go-colorable"
	isatty "github.com/mattn/go-isatty"
	"golang.org/x/term"
)

// IOStreams carries the three standard streams plus terminal metadata.
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer

	// overridable TTY state (used by tests and --no-color flag)
	stdinTTY  *bool
	stdoutTTY *bool
	stderrTTY *bool
	noColor   bool  // set by --no-color flag
	termWidth *int  // override for tests
	human     *bool // set by --format; overrides stdout TTY-ness for layout
	unicode   *bool // override for tests

	passwordReader func(fd int) ([]byte, error)
}

// Format selects how human-facing commands lay out their output.
type Format int

const (
	// FormatAuto lays out tables on a terminal and tab-separated values
	// elsewhere. This is the default and the documented behavior.
	FormatAuto Format = iota
	// FormatTable forces the terminal layout, e.g. when piping to a pager.
	FormatTable
	// FormatTSV forces the tab-separated machine layout on a terminal.
	FormatTSV
)

// ParseFormat maps a --format value to a Format.
func ParseFormat(s string) (Format, error) {
	switch s {
	case "auto":
		return FormatAuto, nil
	case "table":
		return FormatTable, nil
	case "tsv":
		return FormatTSV, nil
	}
	return FormatAuto, fmt.Errorf("invalid --format %q: expected auto, table, or tsv", s)
}

// SetFormat applies an explicit --format choice. FormatAuto restores detection.
func (s *IOStreams) SetFormat(f Format) {
	switch f {
	case FormatTable:
		v := true
		s.human = &v
	case FormatTSV:
		v := false
		s.human = &v
	case FormatAuto:
		s.human = nil
	}
}

// HumanOutput reports whether output should be laid out for a person: aligned
// columns, relative timestamps, human byte sizes. It follows stdout TTY-ness
// unless --format said otherwise.
//
// Layout code must consult this rather than IsStdoutTTY so that one flag moves
// the whole presentation together — a forced table whose cells still held raw
// byte counts and RFC3339 timestamps would be a table in name only.
func (s *IOStreams) HumanOutput() bool {
	if s.human != nil {
		return *s.human
	}
	return s.IsStdoutTTY()
}

// IsStdinTTY reports whether In is a terminal.
func (s *IOStreams) IsStdinTTY() bool {
	if s.stdinTTY != nil {
		return *s.stdinTTY
	}
	if f, ok := s.In.(*os.File); ok {
		return isatty.IsTerminal(f.Fd())
	}
	return false
}

// IsStdoutTTY reports whether Out is a terminal.
func (s *IOStreams) IsStdoutTTY() bool {
	if s.stdoutTTY != nil {
		return *s.stdoutTTY
	}
	if f, ok := s.Out.(*os.File); ok {
		return isatty.IsTerminal(f.Fd())
	}
	return false
}

// IsStderrTTY reports whether ErrOut is a terminal.
func (s *IOStreams) IsStderrTTY() bool {
	if s.stderrTTY != nil {
		return *s.stderrTTY
	}
	if f, ok := s.ErrOut.(*os.File); ok {
		return isatty.IsTerminal(f.Fd())
	}
	return false
}

// SetStdinTTY overrides the automatic TTY detection for In.
func (s *IOStreams) SetStdinTTY(v bool) { s.stdinTTY = &v }

// SetStdoutTTY overrides the automatic TTY detection for Out.
func (s *IOStreams) SetStdoutTTY(v bool) { s.stdoutTTY = &v }

// SetStderrTTY overrides the automatic TTY detection for ErrOut.
func (s *IOStreams) SetStderrTTY(v bool) { s.stderrTTY = &v }

// SetNoColor forces color off regardless of other settings. Called by --no-color.
func (s *IOStreams) SetNoColor(v bool) { s.noColor = v }

// ColorEnabled returns true when the terminal supports color.
//
// Rules (highest priority first):
//  1. --no-color override → false
//  2. CLICOLOR_FORCE set and non-"0" → true
//  3. stdout not a TTY → false
//  4. NO_COLOR set (any value) → false
//  5. TERM == "dumb" → false
//  6. otherwise → true
func (s *IOStreams) ColorEnabled() bool {
	if s.noColor {
		return false
	}
	if v := os.Getenv("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	if !s.IsStdoutTTY() {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return true
}

// SetTerminalWidth overrides the detected terminal width (used by tests).
func (s *IOStreams) SetTerminalWidth(w int) { s.termWidth = &w }

// SetUnicode overrides the detected terminal encoding (used by tests).
func (s *IOStreams) SetUnicode(v bool) { s.unicode = &v }

// RuleChar returns the character to draw horizontal rules with: the box-drawing
// U+2500 when the terminal encoding can render it, otherwise ASCII "-".
//
// A rule spans the full table width, so a wrong guess is repeated on every
// column. The locale environment is the only portable signal for the encoding,
// and when it says nothing this falls back to ASCII rather than betting.
func (s *IOStreams) RuleChar() string {
	if s.unicodeEnabled() {
		return "─"
	}
	return "-"
}

func (s *IOStreams) unicodeEnabled() bool {
	if s.unicode != nil {
		return *s.unicode
	}
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := os.Getenv(key); v != "" {
			up := strings.ToUpper(v)
			return strings.Contains(up, "UTF-8") || strings.Contains(up, "UTF8")
		}
	}
	return false
}

// SetPasswordReader overrides hidden terminal input. It is primarily a test
// seam for commands that must not echo secrets.
func (s *IOStreams) SetPasswordReader(read func(fd int) ([]byte, error)) {
	s.passwordReader = read
}

// ReadLine reads one line from stdin and stops waiting when ctx is canceled.
// The returned line includes its trailing newline, matching bufio.ReadString.
func (s *IOStreams) ReadLine(ctx context.Context) (string, error) {
	return readContext(ctx, func() (string, error) {
		return bufio.NewReader(s.In).ReadString('\n')
	})
}

// ReadPassword reads one line from the terminal with echo disabled. Terminal
// state is restored before the method returns, including on cancellation.
func (s *IOStreams) ReadPassword(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd := -1
	f, fileBacked := s.In.(*os.File)
	if fileBacked {
		fd = int(f.Fd())
	}
	if s.passwordReader != nil {
		return readContext(ctx, func() ([]byte, error) {
			return s.passwordReader(fd)
		})
	}
	if fd < 0 {
		return nil, fmt.Errorf("hidden input requires terminal-backed stdin")
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	restored := false
	defer func() {
		if !restored {
			_ = term.Restore(fd, oldState)
		}
	}()

	raw, readErr := readPasswordLine(ctx, f)
	restoreErr := term.Restore(fd, oldState)
	restored = restoreErr == nil
	if readErr != nil {
		if restoreErr != nil {
			return raw, errors.Join(readErr, fmt.Errorf("restoring terminal input: %w", restoreErr))
		}
		return raw, readErr
	}
	if restoreErr != nil {
		return nil, fmt.Errorf("restoring terminal input: %w", restoreErr)
	}
	return raw, nil
}

func readPasswordLine(ctx context.Context, reader io.Reader) ([]byte, error) {
	var password []byte
	for {
		var b [1]byte
		n, err := readContext(ctx, func() (int, error) {
			return reader.Read(b[:])
		})
		if n > 0 {
			switch b[0] {
			case '\r', '\n':
				return password, nil
			case '\b', 0x7f:
				if len(password) > 0 {
					password = password[:len(password)-1]
				}
			case 0x03: // Ctrl-C is a byte while the terminal is in raw mode.
				return nil, context.Canceled
			case 0x04: // Ctrl-D
				if len(password) == 0 {
					return nil, io.EOF
				}
				return password, nil
			default:
				password = append(password, b[0])
			}
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(password) > 0 {
				return password, nil
			}
			return password, err
		}
	}
}

type readResult[T any] struct {
	value T
	err   error
}

func readContext[T any](ctx context.Context, read func() (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	result := make(chan readResult[T], 1)
	go func() {
		value, err := read()
		result <- readResult[T]{value: value, err: err}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case got := <-result:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return got.value, got.err
	}
}

// TerminalWidth returns the width of the terminal, or 80 when not a TTY.
func (s *IOStreams) TerminalWidth() int {
	if s.termWidth != nil {
		return *s.termWidth
	}
	if f, ok := s.Out.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return 80
}

// System builds an IOStreams backed by the real os.Stdin/Stdout/Stderr.
// On Windows it wraps Stdout/Stderr with go-colorable so ANSI codes work.
func System() *IOStreams {
	return &IOStreams{
		In:     os.Stdin,
		Out:    colorable.NewColorableStdout(),
		ErrOut: colorable.NewColorableStderr(),
	}
}

// Test returns an IOStreams backed by in-memory buffers, plus the three buffers
// so callers can inspect output. TTY flags default to false, and the terminal
// encoding is pinned to UTF-8 so rule glyphs in output goldens never depend on
// the host's locale.
func Test() (streams *IOStreams, in, out, errOut *bytes.Buffer) {
	inBuf := &bytes.Buffer{}
	outBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	f := false
	unicode := true
	streams = &IOStreams{
		In:        inBuf,
		Out:       outBuf,
		ErrOut:    errBuf,
		stdinTTY:  &f,
		stdoutTTY: &f,
		stderrTTY: &f,
		unicode:   &unicode,
	}
	return streams, inBuf, outBuf, errBuf
}
