package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
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

func TestLocalSnapshotSkipsSigningWithoutWeakeningReleases(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	assert.Regexp(t,
		regexp.MustCompile(`(?m)^snapshot:\n\tgoreleaser release --snapshot --clean --skip=sign$`),
		makefile,
		"local snapshots must not require keyless signing credentials")

	config := readRepoFile(t, ".goreleaser.yml")
	assert.Contains(t, config, "artifacts: checksum",
		"real releases must continue signing the checksum manifest")
}

func TestInstallerRejectsInvalidVersionsBeforeNetworkAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("installer is a POSIX shell script")
	}

	binDir := t.TempDir()
	curlMarker := filepath.Join(t.TempDir(), "curl-called")
	fakeCurl := filepath.Join(binDir, "curl")
	require.NoError(t, os.WriteFile(fakeCurl, []byte(
		"#!/bin/sh\n: > \"$CURL_MARKER\"\nexit 99\n"), 0o755))

	for _, version := range []string{
		"1.2.3",
		"v01.2.3",
		"v1.2",
		"v1.2.3-01",
		"v1.2.3/../../escape",
		"v1.2.3;touch-pwned",
	} {
		t.Run(version, func(t *testing.T) {
			require.NoError(t, os.RemoveAll(curlMarker))
			cmd := exec.Command("sh", filepath.Join(repoRoot(t), "script", "install.sh"))
			cmd.Env = append(os.Environ(),
				"MELANGE_VERSION="+version,
				"CURL_MARKER="+curlMarker,
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			output, err := cmd.CombinedOutput()
			require.Error(t, err)
			assert.Contains(t, string(output),
				"MELANGE_VERSION must be a v-prefixed semantic version")
			assert.NoFileExists(t, curlMarker,
				"an invalid version must be rejected before curl can use it")
		})
	}
}

func TestInstallerAcceptsStrictVSemVer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("installer is a POSIX shell script")
	}

	binDir := t.TempDir()
	curlMarker := filepath.Join(t.TempDir(), "curl-called")
	fakeCurl := filepath.Join(binDir, "curl")
	require.NoError(t, os.WriteFile(fakeCurl, []byte(
		"#!/bin/sh\n: > \"$CURL_MARKER\"\nexit 99\n"), 0o755))

	for _, version := range []string{
		"v0.0.0",
		"v1.2.3",
		"v1.2.3-rc.1",
		"v1.2.3+build.5",
		"v1.2.3-rc.1+build.5",
	} {
		t.Run(version, func(t *testing.T) {
			require.NoError(t, os.RemoveAll(curlMarker))
			cmd := exec.Command("sh", filepath.Join(repoRoot(t), "script", "install.sh"))
			cmd.Env = append(os.Environ(),
				"MELANGE_VERSION="+version,
				"CURL_MARKER="+curlMarker,
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			output, err := cmd.CombinedOutput()
			require.Error(t, err, "the fake curl intentionally fails the download")
			assert.NotContains(t, string(output),
				"MELANGE_VERSION must be a v-prefixed semantic version")
			assert.FileExists(t, curlMarker,
				"a valid version must reach the release download")
		})
	}
}

func TestPublishedDocumentationPreservesReleaseContracts(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	readmeQuickStart := section(t, readme, "## Quick start", "## For agents")
	assert.Contains(t, readmeQuickStart,
		`repo="$(melange repo create whisper-tiny --private --jq .full_name)"`)
	assert.Contains(t, readmeQuickStart, `-R "$repo"`)
	assert.Contains(t, readmeQuickStart, "melange usage quotas")
	assert.Contains(t, readmeQuickStart,
		`model_key="$(printf '%s\n' "$upload_json" | jq -er .model.key)"`)
	assert.NotContains(t, readmeQuickStart, "acme/")
	assert.NotRegexp(t, regexp.MustCompile(`\bm_[[:alnum:]]+\b`), readmeQuickStart)

	skill := readRepoFile(t, "skills/melange-cli-usage/SKILL.md")
	skillWorkflow := section(t, skill, "```sh\n# 1. Create a repository", "\n```\n")
	assert.Contains(t, skillWorkflow,
		`repo="$(melange repo create whisper-tiny --private --jq .full_name)"`)
	assert.Contains(t, skillWorkflow, "melange usage quotas")
	assert.Contains(t, skillWorkflow,
		`model_key="$(printf '%s\n' "$upload_json" | jq -er .model.key)"`)
	assert.NotContains(t, skillWorkflow, "acme/")

	llms := readRepoFile(t, "llms.txt")
	for name, contents := range map[string]string{
		"README.md":                         readme,
		"skills/melange-cli-usage/SKILL.md": skill,
		"llms.txt":                          llms,
	} {
		assert.NotContains(t, contents, "byte-for-byte", name)
		assert.NotContains(t, contents, "byte-exact", name)
		assert.Contains(t, contents, "exactly one trailing newline", name)
	}
	assert.NotContains(t, skill, "SDK 1.9.0")
	assert.NotContains(t, llms, "SDK 1.9.0")

	importHelp := readRepoFile(t, "internal/cmd/model/import.go")
	assert.Contains(t, importHelp, "retried automatically within that invocation")
	assert.Contains(t, importHelp, "Running the command again starts a new import request")
	assert.NotContains(t, importHelp, "replaying the same import returns the original model")
}

func TestSecurityPolicyUsesPrivateReporting(t *testing.T) {
	policy := readRepoFile(t, "SECURITY.md")
	assert.Contains(t, policy, "security@zetic.ai")
	assert.Contains(t, policy, "v1")
	assert.Contains(t, strings.ToLower(policy), "do not")
	assert.Contains(t, strings.ToLower(policy), "public issue")
}

func section(t *testing.T, contents, start, end string) string {
	t.Helper()
	startAt := strings.Index(contents, start)
	require.NotEqual(t, -1, startAt, "missing section start %q", start)
	endAt := strings.Index(contents[startAt+len(start):], end)
	require.NotEqual(t, -1, endAt, "missing section end %q", end)
	return contents[startAt : startAt+len(start)+endAt]
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

func TestInstallDocsDoNotAdvertiseUnavailableHomebrewTap(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	assert.NotContains(t, readme, "brew tap zetic-ai/tap")
	assert.NotContains(t, readme, "brew install melange")

	releaseConfig := readRepoFile(t, ".goreleaser.yml")
	assert.NotContains(t, releaseConfig, "homebrew_casks:")
	assert.NotContains(t, releaseConfig, "TAP_GITHUB_TOKEN")

	releaseWorkflow := readRepoFile(t, ".github/workflows/release.yml")
	assert.NotContains(t, releaseWorkflow, "homebrew-tap")
	assert.NotContains(t, releaseWorkflow, "TAP_GITHUB_TOKEN")
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
