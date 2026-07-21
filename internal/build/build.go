// Package build provides version information set at build time via -ldflags.
package build

import "fmt"

// These variables are set at build time via:
//
//	go build -ldflags "-X github.com/zetic-ai/melange-cli/internal/build.Version=v1.0.0 ..."
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Info returns a human-readable version string.
func Info() string {
	return fmt.Sprintf("%s (%s %s)", Version, Commit, Date)
}
