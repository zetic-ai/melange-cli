# Melange CLI

Give your AI agent access to [Melange](https://melange.zetic.ai), an on-device AI deployment platform by [ZETIC](https://zetic.ai). `melange` is designed agents-first: non-interactive safe, structured `--json` output, stable exit codes, and machine-actionable errors.

## Install

### 1. Install the CLI

**Homebrew** (macOS or Linux):

```sh
brew install zetic-ai/tap/melange
```

**npm** (macOS, Linux, or Windows):

```sh
npm install -g @zetic-ai/melange-cli
```

<details>
<summary>Alternatives: Installer script, Go, and manual installation</summary>

The macOS/Linux installer requires
[`cosign`](https://docs.sigstore.dev/cosign/system_config/installation/) and
verifies the release-workflow signature and checksum:

```sh
curl -fsSL https://raw.githubusercontent.com/zetic-ai/melange-cli/main/script/install.sh | sh
```

With a current Go toolchain:

```sh
go install github.com/zetic-ai/melange-cli/cmd/melange@latest
```

Prebuilt binaries for macOS, Linux, and Windows on amd64/arm64, with checksums
and SBOMs, are available on the
[releases page](https://github.com/zetic-ai/melange-cli/releases).

</details>

### 2. Install the agent skill

An [agent skill](https://agentskills.io) is required for driving `melange` from
Claude Code, Codex, Cursor, OpenCode, and other compatible coding agents.

```sh
# Install for universal agents and Claude Code
npx skills add zetic-ai/melange-cli --skill melange-cli \
  --agent universal claude-code --global --yes

# Or choose one or more supported agents interactively
npx skills add zetic-ai/melange-cli --skill melange-cli --global

# Update the installed skill after a melange-cli release
npx skills update melange-cli --global
```

Restart your agent afterward so it can discover the skill.

## Authentication

Get a Personal Access Token at [Melange](https://melange.zetic.ai/settings?tab=pat) (Settings → Personal Access Tokens). Then either export it:

```sh
export MELANGE_API_KEY="ztp_your_personal_access_token"
```

Or store it once:

```sh
melange auth login
```

### Check status

```sh
melange auth status
```

## Use the CLI directly

A read-only path to confirm the install works. Nothing here creates anything or
counts against a quota:

```sh
melange auth login                        # or export MELANGE_API_KEY
melange library list --search whisper     # browse the public model library
melange library view zetic/whisper-tiny   # one model in detail
melange repo list                         # your own repositories
```

Add `--json` (optionally with `--jq`) to any command for machine-readable output.

### Identifiers

Three different identifiers appear in command arguments, and they are not
interchangeable — passing a repository or display name where a `MODEL_KEY` or
`TARGET_ID` belongs is the most common mistake:

| Identifier | What it is | How to get it |
| --- | --- | --- |
| `ACCOUNT/REPO` | A Melange repository address | `melange repo list` |
| `MODEL_KEY` | One converted model version inside a repository | `melange model list -R ACCOUNT/REPO` |
| `TARGET_ID` | One downloadable converted artifact; opaque (`tm_…`/`ltm_…`) | `melange model targets MODEL_KEY -R ACCOUNT/REPO` |

They chain in that order:

```sh
melange model list -R zetic/whisper-tiny
melange model targets MODEL_KEY -R zetic/whisper-tiny
melange model download MODEL_KEY -R zetic/whisper-tiny --target TARGET_ID --yes
```

`melange model download` is billable, so it requires `--yes` when it cannot ask
interactively.

## Build with AI

### What's supported
With the agent skill installed, your agent can:

- Search the public model library and inspect available model versions.
- Compare real device benchmarks, targets, and report availability.
- Create and manage repositories, imports, uploads, and model versions.
- Generate exact deployment guides for Android, iOS, and Flutter.
- Check authentication, usage, quotas, and plan-specific availability.

### Quick start (example prompts)

Discover a public model and deploy

```sh
Find a computer vision model by Meta in the Melange model library,
review its real device benchmark results, and give me the iOS deployment code/guide.
```

Upload your own model

```sh
I want to upload model.pt2 with sample.npy to Melange.
Upload the model, monitor conversion, and report the final status without retrying implicitly.
```

Compare benchmarks

```sh
Compare Gemma4 with another llm available in Melange. Use only benchmark values returned by Melange.
Show throughput and peak memory for iPhone 16 and Galaxy S25 where available.
```

## Update 

### Update the CLI

**Homebrew** (macOS or Linux):

```sh
brew upgrade zetic-ai/tap/melange
```

**npm** (macOS, Linux, or Windows):

```sh
npm update -g @zetic-ai/melange-cli
```

### Update the agent skill

```sh
npx skills update melange-cli --global
```

## Documentation

- Command reference (generated): [`docs/reference/melange.md`](docs/reference/melange.md)
- LLM/agent surface reference: [`llms.txt`](llms.txt)

## License

Apache-2.0
