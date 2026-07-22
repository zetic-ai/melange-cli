package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), name))
	require.NoError(t, err)
	return string(raw)
}

func TestReleaseChecksumsAreKeylesslySignedAndInstallerVerifiesThem(t *testing.T) {
	config := readRepoFile(t, ".goreleaser.yml")
	assert.Contains(t, config, "artifacts: checksum")
	assert.Contains(t, config, "--bundle=${signature}")

	workflow := readRepoFile(t, ".github/workflows/release.yml")
	assert.Contains(t, workflow, "id-token: write")
	assert.Contains(t, workflow, "sigstore/cosign-installer@")

	installer := readRepoFile(t, "script/install.sh")
	verifyAt := strings.Index(installer, "cosign verify-blob")
	checksumAt := strings.Index(installer, `[ "$expected" = "$actual" ]`)
	require.NotEqual(t, -1, verifyAt, "installer must verify the signed checksum manifest")
	require.NotEqual(t, -1, checksumAt)
	assert.Less(t, verifyAt, checksumAt,
		"the checksum manifest's identity must be authenticated before trusting its digest")
	assert.Contains(t, installer,
		`https://github.com/zetic-ai/melange-cli/.github/workflows/release.yml@refs/tags/${version}`)
	assert.Contains(t, installer, "https://token.actions.githubusercontent.com")
}

func TestGitHubActionsArePinnedByCommit(t *testing.T) {
	majorTag := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*[^\s]+@v[0-9]+(?:\s|$)`)
	for _, name := range []string{".github/workflows/ci.yml", ".github/workflows/release.yml"} {
		contents := readRepoFile(t, name)
		assert.NotRegexp(t, majorTag, contents, "%s must pin actions by immutable commit SHA", name)
	}
}

func TestTagReleaseIsGatedByRepositoryChecks(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/release.yml")

	for _, check := range []string{
		"go build ./...",
		"go test -race ./...",
		"golangci/golangci-lint-action@",
		"make gen-check",
		"make docs-check",
	} {
		assert.Contains(t, workflow, check,
			"a tag must not publish without the %q release check", check)
	}
	assert.Regexp(t, regexp.MustCompile(`(?m)^\s+needs:\s+verify\s*$`), workflow,
		"the publishing job must depend on the verification job")
	for _, osName := range []string{"ubuntu-latest", "macos-latest", "windows-latest"} {
		assert.Contains(t, workflow, osName,
			"the exact tag must be tested on %s before publishing", osName)
	}
}

func TestHomebrewInstallDoesNotClaimUnsupportedLinuxCask(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	assert.Contains(t, readme, "**Homebrew** (macOS):")
	assert.NotContains(t, readme, "**Homebrew** (macOS/Linux):")
}

func TestOpenAPISourceProvenanceMatchesCommittedArtifact(t *testing.T) {
	source := readRepoFile(t, "openapi/SOURCE")
	var values []string
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			values = append(values, line)
		}
	}
	require.Len(t, values, 2, "SOURCE must contain backend commit and spec digest")
	commit, err := hex.DecodeString(values[0])
	require.NoError(t, err)
	require.Len(t, commit, 20, "backend provenance must be a full Git SHA")
	expected, err := hex.DecodeString(values[1])
	require.NoError(t, err)
	require.Len(t, expected, sha256.Size)

	spec, err := os.ReadFile(filepath.Join(repoRoot(t), "openapi/public-v1.json"))
	require.NoError(t, err)
	actual := sha256.Sum256(spec)
	assert.Equal(t, expected, actual[:], "public OpenAPI artifact drifted from SOURCE")
}
