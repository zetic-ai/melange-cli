# Melange CLI

`melange` is the public command-line interface for [Melange](https://melange.zetic.ai), an on-device AI deployment platform by [ZETIC](https://zetic.ai).

It is designed agents-first: non-interactive safe, structured `--json` output, stable exit codes, and machine-actionable errors. Give your AI agent access to Melange.

## Install

**Installer script** (macOS/Linux; requires
[`cosign`](https://docs.sigstore.dev/cosign/system_config/installation/),
verifies the release-workflow signature and checksum, then installs to
`/usr/local/bin` or `~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/zetic-ai/melange-cli/main/script/install.sh | sh
```

**Go**:

```sh
go install github.com/zetic-ai/melange-cli/cmd/melange@latest
```

Prebuilt binaries (darwin/linux/windows, amd64/arm64) with checksums and SBOMs are on the [releases page](https://github.com/zetic-ai/melange-cli/releases).

## Agent skills

An [agent skill](https://agentskills.io) is available for driving `melange` from
Claude Code, Codex, Cursor, OpenCode, and other compatible coding agents. Install it at
user scope with the open Agent Skills installer:

```sh
# Install the skill (user scope recommended; select an agent when prompted)
npx skills add zetic-ai/melange-cli --skill melange-cli --global

# Or install non-interactively for Claude Code
npx skills add zetic-ai/melange-cli --skill melange-cli --agent claude-code --global --yes

# Update the installed skill after a melange-cli release
npx skills update melange-cli --global
```

Installing the `melange` binary does not install or update the agent skill.
Restart your agent after installation so it can discover the skill.

## Authentication

Create a Personal Access Token in Melange → Settings → Personal Access Tokens.

Login with your Melange personal access token (stored securely across platforms):

```sh
melange auth login
```

Or set environment variable:

```sh
export MELANGE_API_KEY="ztp_your_personal_access_token"
```

### Check status

```sh
melange auth status
```

## Quick start

```sh
repo="$(melange repo create whisper-tiny --private --jq .full_name)"
melange model upload -R "$repo" model.onnx --input audio.bin --dry-run --json

upload_limit="$(melange usage quotas --jq '.model_uploads.limit // "unlimited"')" || exit $?
[ "$upload_limit" != "0" ] || { echo "Model uploads are unavailable for this account" >&2; exit 1; }
upload_json="$(melange model upload -R "$repo" model.onnx --input audio.bin --wait --json)" || exit $?
model_key="$(printf '%s\n' "$upload_json" | jq -er .model.key)" || exit $?

melange model status "$model_key" -R "$repo" --json
melange deploy guide "$model_key" -R "$repo" --language android-kotlin --mode auto
melange api /v1/me --jq .account.name    # any /v1 endpoint
```

Bucketed `.pt2` uploads use repeatable `--bucket INDEX:DIMxDIM...` declarations.
Inputs are grouped by bucket declaration order; preview the exact manifest with
`--dry-run --json` before uploading.

## For agents

- Authenticate via env: `export MELANGE_API_KEY=ztp_...` (overrides stored credentials); verify with `melange auth status --json`.
- Always use `--json` or `--jq` — never parse TTY tables. Plain `--json` preserves the API response bytes except for normalizing the terminator to exactly one trailing newline; waited upload/import commands instead compose `{"model": ..., "status": ...}`, and `model download` redacts signed artifact URLs. Data is on stdout; progress/diagnostics on stderr.
- Billable `model download` commands keep a host/repository/model/target-bound authorization key in per-user application state and serialize concurrent processes. Output corrections, failed followers, and directory collisions retain the key; follow the reported directory/`--force` remediation without another charge.
- Branch on stable exit codes: `0` ok, `1` failure (possibly transient), `2` usage error (fix the command), `4` auth, `130` interrupted (upload session preserved).
- Load the Melange CLI skill at [`skills/melange-cli/SKILL.md`](skills/melange-cli/SKILL.md), or the compact surface reference in [`llms.txt`](llms.txt).
- Built-in topics: `melange help environment`, `melange help exit-codes`, `melange help formatting`.
- Get credential-safe SDK code with `melange deploy options` and `melange deploy guide`; guides use `YOUR_PERSONAL_KEY` and never interpolate the active PAT.

## Documentation

- Command reference (generated): [`docs/reference/melange.md`](docs/reference/melange.md)
- LLM/agent surface reference: [`llms.txt`](llms.txt)

## Development

Requires Go (see `go.mod`) and `make`:

| Target | What it does |
|--------|--------------|
| `make build` | Build `bin/melange` with version ldflags |
| `make test` | `go test ./...` |
| `make lint` | `golangci-lint run` |
| `make fmt` | `gofmt -l -w .` |
| `make gen` / `make gen-check` | Regenerate / verify the OpenAPI client |
| `make docs` / `make docs-check` | Regenerate / verify `docs/reference` |
| `make snapshot` | Build a local unsigned GoReleaser snapshot |

Releases are built and signed by [GoReleaser](.goreleaser.yml) via the [release workflow](.github/workflows/release.yml) on `v*` tags. `make snapshot` reproduces the build locally while skipping signing, which requires the tag-triggered GitHub Actions identity.

## License

Apache-2.0
