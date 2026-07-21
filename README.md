# melange-cli

`melange` — the public command-line interface for [Melange](https://melange.zetic.ai), zetic.ai's on-device AI model deployment and benchmarking platform.

Designed agents-first: non-interactive safe, structured `--json` output, stable exit codes, and machine-actionable errors — with a pleasant TTY experience for humans.

## Install

**Homebrew** (macOS/Linux):

```sh
brew tap zetic-ai/tap
brew install melange
```

**Installer script** (macOS/Linux; verifies checksums, installs to `/usr/local/bin` or `~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/zetic-ai/melange-cli/main/script/install.sh | sh
```

**Go**:

```sh
go install github.com/zetic-ai/melange-cli/cmd/melange@latest
```

Prebuilt binaries (darwin/linux/windows, amd64/arm64) with checksums and SBOMs are on the [releases page](https://github.com/zetic-ai/melange-cli/releases).

## Authentication

Create a Personal Access Token in Melange → Settings → Personal Access Tokens, then:

```sh
melange auth login            # paste token interactively
melange auth status           # verify who you are
```

## Quick start

```sh
melange repo create whisper-tiny --private
melange model upload -R acme/whisper-tiny model.onnx --input audio.bin --wait
melange model status m_ab12cd -R acme/whisper-tiny
melange api /v1/me --jq .account.name    # any /v1 endpoint
```

## For agents

- Authenticate via env: `export MELANGE_API_KEY=ztp_...` (overrides stored credentials); verify with `melange auth status --json`.
- Always use `--json` (byte-exact API payload) or `--jq` — never parse TTY tables. Data is on stdout; progress/diagnostics on stderr.
- Branch on stable exit codes: `0` ok, `1` failure (possibly transient), `2` usage error (fix the command), `4` auth, `130` interrupted (upload session preserved).
- Load the usage skill at [`skills/melange-cli-usage/SKILL.md`](skills/melange-cli-usage/SKILL.md), or the compact surface reference in [`llms.txt`](llms.txt).
- Built-in topics: `melange help environment`, `melange help exit-codes`, `melange help formatting`.

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

Releases are built by [GoReleaser](.goreleaser.yml) via the [release workflow](.github/workflows/release.yml) on `v*` tags; `goreleaser release --snapshot --clean` reproduces the pipeline locally.

## License

Apache-2.0
