# Melange CLI

`melange` is the public command-line interface for [Melange](https://melange.zetic.ai), an on-device AI deployment platform by [ZETIC](https://zetic.ai).

It is designed agents-first: non-interactive safe, structured `--json` output, stable exit codes, and machine-actionable errors. Give your AI agent access to Melange.

## Install

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

## Agent skills

An [agent skill](https://agentskills.io) is available for driving `melange` from
Claude Code, Codex, Cursor, OpenCode, and other compatible coding agents.,

```sh
# Install for universal agents and Claude Code
npx skills add zetic-ai/melange-cli --skill melange-cli \
  --agent universal claude-code --global --yes

# Or choose one or more supported agents interactively
npx skills add zetic-ai/melange-cli --skill melange-cli --global

# Update the installed skill after a melange-cli release
npx skills update melange-cli --global
```

Installing the `melange` binary does not install or update the agent skill.
Restart your agent after installation so it can discover the skill.

## Authentication

Create a Personal Access Token in Melange → Settings → Personal Access Tokens. Get it [here](https://melange.zetic.ai/settings?tab=pat), then:

Set environment variable with your Melange PAT:

```sh
export MELANGE_API_KEY="ztp_your_personal_access_token"
```

Or login (stored securely across platforms):

```sh
melange auth login
```

### Check status

```sh
melange auth status
```

## Build with AI

### What's supported
With the agent skill installed, your agent can:

- Search the public model library and inspect available model versions.
- Compare real device benchmarks, targets, and report availability.
- Create and manage repositories, imports, uploads, and model versions.
- Generate exact deployment guides for Android, iOS, and Flutter.
- Check authentication, usage, quotas, and plan-specific availability.

### Quick start

Discover and deploy

```sh
Find a computer vision model by Meta in the Melange model library,
review its real device benchmark results, and give me the iOS deployment code/guide.
```

Upload a model

```sh
I want to upload model.pt2 with sample.npy to Melange.
Upload the model, monitor conversion, and report the final status without retrying implicitly.
```

Compare benchmarks

```sh
Compare Gemma4 with another llm available in Melange. Use only benchmark values returned by Melange.
Show throughput and peak memory for iPhone 16 and Galaxy S25 where available.
```

## Documentation

- Command reference (generated): [`docs/reference/melange.md`](docs/reference/melange.md)
- LLM/agent surface reference: [`llms.txt`](llms.txt)

## License

Apache-2.0
