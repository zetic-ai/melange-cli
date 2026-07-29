// Package text provides small formatting helpers shared by command output:
// rune-safe truncation, compact relative timestamps, and byte sizes.
package text

import (
	"fmt"
	"strings"
	"time"
)

const ellipsis = "..."

var tsvEscaper = strings.NewReplacer(
	`\`, `\\`,
	"\t", `\t`,
	"\r", `\r`,
	"\n", `\n`,
)

// EscapeTSVCell makes one value safe for the CLI's line-oriented TSV
// contract. Printable content is unchanged; backslashes and record/column
// control characters use reversible backslash escapes.
func EscapeTSVCell(s string) string {
	return tsvEscaper.Replace(sanitizeTerminal(s, true))
}

// SanitizeTerminal removes terminal escape/control sequences from untrusted
// human-readable text. Newlines and tabs are retained so block layout remains
// readable; all other C0/C1 controls are removed. OSC, CSI, and related string
// controls are consumed through their terminator so their payload cannot be
// reinterpreted or copied into a terminal.
func SanitizeTerminal(s string) string {
	return sanitizeTerminal(s, false)
}

func sanitizeTerminal(s string, preserveRecordControls bool) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '\x1b':
			if i+1 >= len(runes) {
				continue
			}
			i++
			switch runes[i] {
			case '[':
				i = skipCSI(runes, i+1)
			case ']', 'P', 'X', '^', '_':
				i = skipControlString(runes, i+1)
			default:
				// Two-byte escape sequence: consume its final byte.
			}
		case '\u009b':
			i = skipCSI(runes, i+1)
		case '\u0090', '\u0098', '\u009d', '\u009e', '\u009f':
			i = skipControlString(runes, i+1)
		case '\n', '\t':
			b.WriteRune(r)
		case '\r':
			if preserveRecordControls {
				b.WriteRune(r)
			}
		default:
			if r < '\x20' || (r >= '\x7f' && r <= '\u009f') {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SanitizeTerminalInline is SanitizeTerminal for table cells and other
// single-line fields. Layout controls become spaces rather than new rows.
func SanitizeTerminalInline(s string) string {
	s = SanitizeTerminal(s)
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\t", " ")
}

func skipCSI(runes []rune, start int) int {
	for i := start; i < len(runes); i++ {
		if runes[i] >= '\x40' && runes[i] <= '\x7e' {
			return i
		}
	}
	return len(runes) - 1
}

func skipControlString(runes []rune, start int) int {
	for i := start; i < len(runes); i++ {
		switch runes[i] {
		case '\a', '\u009c':
			return i
		case '\x1b':
			if i+1 < len(runes) && runes[i+1] == '\\' {
				return i + 1
			}
		}
	}
	return len(runes) - 1
}

// Truncate shortens s to at most max terminal columns (see DisplayWidth),
// appending "..." when content is cut. It never splits a multibyte rune. A max
// at or below the width of the ellipsis truncates without the suffix;
// max <= 0 yields "".
func Truncate(max int, s string) string {
	if max <= 0 {
		return ""
	}
	if DisplayWidth(s) <= max {
		return s
	}
	if max <= len(ellipsis) {
		return cutToWidth(s, max)
	}
	return cutToWidth(s, max-len(ellipsis)) + ellipsis
}

// Pluralize renders a count with its noun: "1 repository", "3 repositories".
// Both forms are spelled out by the caller because English plurals the CLI
// needs are not all formed by appending "s".
func Pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// RelativeTime renders t relative to now in a compact form: "just now",
// "5m ago", "3h ago", "2d ago", and an absolute "Jan 2, 2006" date beyond
// 30 days. Future timestamps render as "just now".
func RelativeTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d <= 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("Jan 2, 2006")
}

// FormatBytes renders n in binary units (KiB/MiB/GiB) with one decimal.
// Values below 1 KiB are plain bytes; values beyond 1 GiB stay in GiB.
func FormatBytes(n int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case n < kib:
		return fmt.Sprintf("%d B", n)
	case n < mib:
		return fmt.Sprintf("%.1f KiB", float64(n)/kib)
	case n < gib:
		return fmt.Sprintf("%.1f MiB", float64(n)/mib)
	}
	return fmt.Sprintf("%.1f GiB", float64(n)/gib)
}
