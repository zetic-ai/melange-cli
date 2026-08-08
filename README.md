# Melange CLI

<p align="left">
  <img src="melange-cli.png" alt="Melange CLI" width="400" />
</p>

Give your AI agent access to [Melange](https://melange.zetic.ai), an on-device AI deployment platform by [ZETIC](https://zetic.ai). `melange` is designed agents-first: non-interactive safe, structured `--json` output, stable exit codes, and machine-actionable errors.

## Install

One line on macOS or Linux — installs the CLI and the
[agent skill](https://agentskills.io) that drives it:

```sh
curl -fsSL https://raw.githubusercontent.com/zetic-ai/melange-cli/main/script/install.sh | sh
```

Restart your coding agent afterward so it can discover the skill. Re-run the
same line any time to update both.

The installer downloads the release binary for your platform, verifies its
SHA-256 checksum, and installs the skill for universal agents and Claude Code.
When [`cosign`](https://docs.sigstore.dev/cosign/system_config/installation/) is
on your PATH it also verifies the release-workflow signature; pass
`| sh -s -- --require-signature` to make that mandatory.

<details>
<summary>Installer options</summary>

Append flags after `sh -s --`, or set the matching environment variable:

| Flag | Environment variable | Effect |
| --- | --- | --- |
| `--version v1.2.3` | `MELANGE_VERSION` | Install a specific release instead of the latest |
| `--install-dir DIR` | `MELANGE_INSTALL_DIR` | Binary directory; defaults to `/usr/local/bin`, falling back to `~/.local/bin` |
| `--cli-only` | `MELANGE_SKIP_SKILL=1` | Skip the agent skill |
| `--skill-only` | `MELANGE_SKIP_CLI=1` | Skip the CLI |
| `--agent "A B"` | `MELANGE_SKILL_AGENTS` | Agents to install the skill for; defaults to `universal claude-code` |
| `--require-signature` | `MELANGE_REQUIRE_SIGNATURE=1` | Fail unless the release signature is verified |

```sh
curl -fsSL https://raw.githubusercontent.com/zetic-ai/melange-cli/main/script/install.sh \
  | sh -s -- --version v1.2.3 --agent "universal claude-code codex"
```

The skill is installed with [`npx skills`](https://github.com/vercel-labs/skills)
when a recent Node is available, and copied straight into the agent skill
directories otherwise.

</details>

<details>
<summary>Alternatives: Homebrew, npm, Go, and manual installation</summary>

These install the CLI only — add the agent skill separately with the
`npx skills add` command below.

**Homebrew** (macOS or Linux):

```sh
brew install zetic-ai/tap/melange
```

**npm** (macOS, Linux, or Windows — the only supported path on Windows):

```sh
npm install -g @zetic-ai/melange-cli
```

With a current Go toolchain:

```sh
go install github.com/zetic-ai/melange-cli/cmd/melange@latest
```

Prebuilt binaries for macOS, Linux, and Windows on amd64/arm64, with checksums
and SBOMs, are available on the
[releases page](https://github.com/zetic-ai/melange-cli/releases).

The agent skill, for driving `melange` from Claude Code, Codex, Cursor,
OpenCode, and other compatible coding agents:

```sh
# Install for universal agents and Claude Code
npx skills add zetic-ai/melange-cli --skill melange-cli \
  --agent universal claude-code --global --yes

# Or choose one or more supported agents interactively
npx skills add zetic-ai/melange-cli --skill melange-cli --global
```

Restart your agent afterward so it can discover the skill.

</details>

## Authentication

By default `melange auth login` opens a browser for OAuth (recommended, `zoa_`/`zor_` stored in OS keyring and auto-refreshed). For CI/headless use a Personal Access Token (`ztp_`) from [Melange Settings → Personal Access Tokens](https://melange.zetic.ai/settings?tab=pat):

```sh
export MELANGE_API_KEY="ztp_your_personal_access_token"
# or
melange auth login --with-token < token.txt
```

Or store it interactively once:

```sh
melange auth login   # browser OAuth; falls back to PAT paste if browser unavailable
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

## MCP server

The CLI ships a built-in [MCP](https://modelcontextprotocol.io) server:
`melange mcp` serves 18 tools over stdio (17 over HTTP — `upload_model` needs
the caller's local files) so MCP clients call Melange directly instead of
shelling out. The stdio server reuses the CLI's credentials (`MELANGE_API_KEY`
or `melange auth login`), resolved lazily on the first tool call. Install the
CLI first (see [Install](#install)) so `melange` is on your `PATH`, then
register it with your client. The full tool catalog and per-transport details
are in [`llms.txt`](llms.txt).

### Claude Code

```sh
claude mcp add melange -- melange mcp
```

Verify with `claude mcp list` — the entry should show `✔ Connected`.

### Claude Desktop

Add to `claude_desktop_config.json` (Settings → Developer → Edit Config;
macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`,
Windows: `%APPDATA%\Claude\claude_desktop_config.json`), then restart
Claude Desktop:

```json
{
  "mcpServers": {
    "melange": {
      "command": "melange",
      "args": ["mcp"]
    }
  }
}
```

### Cursor

Add to `.cursor/mcp.json` in your project (or `~/.cursor/mcp.json` for all
projects):

```json
{
  "mcpServers": {
    "melange": {
      "command": "melange",
      "args": ["mcp"]
    }
  }
}
```

### Remote (Streamable HTTP)

For remote agent clients, serve the Streamable HTTP transport. The server
itself holds no credentials: every request must carry its own token as
`Authorization: Bearer <token>`, so one deployment serves many callers.

The server speaks plain HTTP; terminate TLS in front of it (load balancer,
reverse proxy, or ingress). The `https://` client URLs below assume that.

```sh
melange mcp --transport http --listen 0.0.0.0:8080
```

Claude Code (PAT `ztp_...` or OAuth `zoa_...`):

```sh
claude mcp add --transport http melange https://your-host:8080/ \
  --header "Authorization: Bearer ztp_your_personal_access_token" # or zoa_... OAuth
```

Cursor (`.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "melange": {
      "url": "https://your-host:8080/",
      "headers": {
        "Authorization": "Bearer ztp_your_personal_access_token" // or zoa_... OAuth
      }
    }
  }
}
```

Claude Desktop registers local stdio servers through
`claude_desktop_config.json` (above); remote servers are added as custom
connectors in claude.ai settings instead. See `melange mcp --help` for the
HTTP deployment flags (`--validate-tokens`, `--allowed-origins`,
`--resource`).

## Update 

Re-run the installer to update the CLI and the skill together:

```sh
curl -fsSL https://raw.githubusercontent.com/zetic-ai/melange-cli/main/script/install.sh | sh
```

If you installed the CLI another way, update it the same way you installed it —
`brew upgrade zetic-ai/tap/melange` or `npm update -g @zetic-ai/melange-cli` —
and update the skill with `npx skills update melange-cli --global`.

## Documentation

- Command reference (generated): [`docs/reference/melange.md`](docs/reference/melange.md)
- LLM/agent surface reference: [`llms.txt`](llms.txt)

## License

Apache-2.0
