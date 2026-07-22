// Package build provides version information set at build time via -ldflags.
package build

import (
	"fmt"
	"runtime/debug"
)

// These variables are set at build time via:
//
//	go build -ldflags "-X github.com/zetic-ai/melange-cli/internal/build.Version=v1.0.0 ..."
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		Version = resolveVersion(Version, info.Main.Version)
	}
}

func resolveVersion(injected, module string) string {
	if injected == "dev" && module != "" && module != "(devel)" {
		return module
	}
	return injected
}

// Info returns a human-readable version string.
func Info() string {
	return fmt.Sprintf("%s (%s %s)", Version, Commit, Date)
}
