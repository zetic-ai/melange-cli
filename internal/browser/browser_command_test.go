package browser

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCommandForOS(t *testing.T) {
	cases := []struct {
		goos string
		want string
	}{
		{"darwin", "open"},
		{"windows", "cmd"},
		{"linux", "xdg-open"},
		{"freebsd", "xdg-open"},
	}
	for _, tc := range cases {
		cmd := commandForOS(tc.goos, "https://example.com")
		assert.NotNil(t, cmd)
		// First arg is the executable name (may be full path but base contains want)
		assert.Contains(t, cmd.Path, tc.want)
	}
}
