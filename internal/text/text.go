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
	return tsvEscaper.Replace(s)
}

// Truncate shortens s to at most max runes, appending "..." when content is
// cut. It never splits a multibyte rune. A max at or below the width of the
// ellipsis truncates without the suffix; max <= 0 yields "".
func Truncate(max int, s string) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= len(ellipsis) {
		return string(runes[:max])
	}
	return string(runes[:max-len(ellipsis)]) + ellipsis
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
