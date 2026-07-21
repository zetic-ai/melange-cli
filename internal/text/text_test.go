package text_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/zetic-ai/melange-cli/internal/text"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		max  int
		in   string
		want string
	}{
		{"empty string", 10, "", ""},
		{"fits exactly", 5, "hello", "hello"},
		{"shorter than max", 10, "hi", "hi"},
		{"truncated with ellipsis", 8, "melange-cli", "melan..."},
		{"max equals ellipsis width", 3, "hello", "hel"},
		{"max below ellipsis width", 2, "hello", "he"},
		{"max one", 1, "hello", "h"},
		{"max zero", 0, "hello", ""},
		{"negative max", -1, "hello", ""},
		{"multibyte runes not split", 5, "héllo wörld", "hé..."},
		{"multibyte fits", 4, "héll", "héll"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, text.Truncate(tt.max, tt.in))
		})
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"seconds ago", now.Add(-30 * time.Second), "just now"},
		{"in the future", now.Add(2 * time.Hour), "just now"},
		{"one minute", now.Add(-time.Minute), "1m ago"},
		{"minutes", now.Add(-59 * time.Minute), "59m ago"},
		{"one hour", now.Add(-time.Hour), "1h ago"},
		{"hours", now.Add(-23 * time.Hour), "23h ago"},
		{"one day", now.Add(-24 * time.Hour), "1d ago"},
		{"thirty days", now.Add(-30 * 24 * time.Hour), "30d ago"},
		{"beyond thirty days", now.Add(-31 * 24 * time.Hour), "Jun 20, 2026"},
		{"a year ago", now.AddDate(-1, 0, 0), "Jul 21, 2025"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, text.RelativeTime(tt.t, now))
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"bytes", 512, "512 B"},
		{"just below a KiB", 1023, "1023 B"},
		{"one KiB", 1024, "1.0 KiB"},
		{"one and a half KiB", 1536, "1.5 KiB"},
		{"one MiB", 1024 * 1024, "1.0 MiB"},
		{"seven point three MiB", 7654321, "7.3 MiB"},
		{"one GiB", 1024 * 1024 * 1024, "1.0 GiB"},
		{"beyond GiB stays in GiB", 5 * 1024 * 1024 * 1024 * 1024, "5120.0 GiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, text.FormatBytes(tt.n))
		})
	}
}
