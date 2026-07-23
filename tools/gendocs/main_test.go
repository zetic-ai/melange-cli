package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratedMarkdownEndsWithExactlyOneNewline(t *testing.T) {
	outDir := t.TempDir()
	require.NoError(t, run(outDir))

	require.NoError(t, filepath.WalkDir(outDir, func(path string, entry fs.DirEntry, err error) error {
		require.NoError(t, err)
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		contents := string(raw)
		assert.True(t, strings.HasSuffix(contents, "\n"), "%s must end in a newline", path)
		assert.False(t, strings.HasSuffix(contents, "\n\n"),
			"%s must not end in a blank line", path)
		return nil
	}))
}
