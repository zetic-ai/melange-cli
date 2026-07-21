# melange-cli

`melange` — the public command-line interface for [Melange](https://melange.zetic.ai), zetic.ai's on-device AI model deployment and benchmarking platform.

Designed agents-first: non-interactive safe, structured `--json` output, stable exit codes, and machine-actionable errors — with a pleasant TTY experience for humans.

> Status: pre-release. The public API contract (`/v1`) and this CLI are under active development. See the spec repo's `docs/SPEC.md` for progress.

## Install

Coming soon: Homebrew tap, curl installer, and GitHub Releases binaries (darwin/linux/windows, amd64/arm64).

## Authentication

Create a Personal Access Token in Melange → Settings → Personal Access Tokens, then:

```sh
melange auth login            # paste token interactively
# or, for agents/CI:
export MELANGE_API_KEY=ztp_...
```

## License

Apache-2.0
