// Command melange-qualcomm is the Qualcomm-focused Melange CLI entry point.
package main

import (
	"os"

	"github.com/zetic-ai/melange-cli/internal/cliapp"
	"github.com/zetic-ai/melange-cli/internal/edition"
)

func main() {
	os.Exit(Run(os.Args[1:]))
}

// Run executes the Qualcomm edition and returns its stable process exit code.
func Run(args []string) int {
	return cliapp.Run(args, edition.Qualcomm())
}
