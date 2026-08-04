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

```sh
melange mcp --transport http --listen 0.0.0.0:8080
```

Claude Code:

```sh
claude mcp add --transport http melange https://your-host:8080/ \
  --header "Authorization: Bearer ztp_your_personal_access_token"
```

Cursor (`.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "melange": {
      "url": "https://your-host:8080/",
      "headers": {
        "Authorization": "Bearer ztp_your_personal_access_token"
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
