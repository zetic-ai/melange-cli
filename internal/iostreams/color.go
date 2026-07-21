package iostreams

// ColorScheme renders ANSI color sequences when color is enabled and returns
// the input unchanged otherwise. Obtain one via IOStreams.ColorScheme so the
// --no-color flag and NO_COLOR/TERM detection are respected.
type ColorScheme struct {
	enabled bool
}

// ColorScheme returns a scheme bound to the streams' current color setting.
func (s *IOStreams) ColorScheme() *ColorScheme {
	return &ColorScheme{enabled: s.ColorEnabled()}
}

// Enabled reports whether the scheme emits ANSI sequences.
func (c *ColorScheme) Enabled() bool { return c.enabled }

func (c *ColorScheme) wrap(code, s string) string {
	if !c.enabled || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Yellow colors s yellow (used e.g. for "private" visibility).
func (c *ColorScheme) Yellow(s string) string { return c.wrap("33", s) }

// Green colors s green (used e.g. for the "ready" model state).
func (c *ColorScheme) Green(s string) string { return c.wrap("32", s) }

// Red colors s red (used e.g. for the "failed" model state).
func (c *ColorScheme) Red(s string) string { return c.wrap("31", s) }

// Dim renders s faint (used for table headers).
func (c *ColorScheme) Dim(s string) string { return c.wrap("2", s) }
