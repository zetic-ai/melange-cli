package text

import (
	"unicode"

	"golang.org/x/text/width"
)

// DisplayWidth reports how many terminal columns s occupies: East Asian wide
// and fullwidth runes take two, combining marks and format characters take
// none, everything else takes one.
//
// Column alignment must be computed with this rather than a rune count. Melange
// output carries server-supplied text — device marketing names above all — and a
// single CJK character in one cell shifts every column to its right when padding
// is counted in runes.
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	// Marks compose onto the preceding glyph and format characters (ZWSP, BOM,
	// bidi controls) render as nothing, so neither advances the cursor.
	if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// cutToWidth returns the longest prefix of s that fits within w columns without
// splitting a rune. A double-width rune straddling the limit is dropped whole,
// so the result may be one column narrower than w.
func cutToWidth(s string, w int) string {
	used := 0
	for i, r := range s {
		rw := runeWidth(r)
		if used+rw > w {
			return s[:i]
		}
		used += rw
	}
	return s
}
