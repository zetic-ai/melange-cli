# MCP connector-directory submission prep

Status: **preparation only — nothing here is submitted.** This records what
Anthropic's Claude Connectors Directory and OpenAI's ChatGPT connector flow
require (as published on 2026-08-04), what this project has today, and the
concrete gaps before a submission could be made.

Sources were fetched, not recalled:

- Anthropic: <https://claude.com/docs/connectors/building/submission>
- OpenAI: <https://developers.openai.com/api/docs/mcp> and
  <https://developers.openai.com/apps-sdk/deploy>
  (the help-center article on developer mode returned HTTP 403 to our fetch,
  so claims below rely only on the developer-docs pages)
- MCP registry: schema
  `https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json`
  and `docs/reference/server-json/official-registry-requirements.md` plus the
  publishing quickstart in
  <https://github.com/modelcontextprotocol/registry>

## What Anthropic's Connectors Directory requires

From the submission page:

- **Submission channel**: remote MCP servers are submitted through the portal
  at `claude.ai/admin-settings/directory/submissions/new`, which requires a
  **Team or Enterprise organization** and directory-management access
  (Owners by default). Local servers are distributed as desktop extensions
  (MCPB bundles) via a separate form instead.
- **Server**: `https://` URL, streamable HTTP or SSE transport.
- **Tool annotations**: every tool must include a `title` and the applicable
  `readOnlyHint` or `destructiveHint`.
- **Authentication**: OAuth 2.0 for authenticated services — OAuth with
  dynamic client registration, client ID metadata documents, or a static
  client ID held by Anthropic.
- **Privacy policy**: required; must cover data collection, usage and
  storage, third-party sharing, retention, and contact information. Missing
  or incomplete policies are an immediate rejection.
- **Listing collateral**: documentation URL, privacy policy URL, support
  contact, icon, name (≤100 chars), tagline (≤55 chars), description
  (≤2,000 chars), 1–5 categories, and a permanent URL slug.
- **Review access**: test-account setup detailed enough for a reviewer to
  exercise the server end to end, plus confirmation the submitter ran every
  tool (MCP Inspector or a custom connector).
- **Compliance**: agreement to the Anthropic Software Directory Terms and
  Policy and seven policy acknowledgments in the portal.

## What ChatGPT's connector flow requires

From OpenAI's developer docs:

- **Endpoint**: a stable, publicly reachable **HTTPS** endpoint speaking the
  MCP streamable HTTP transport (deep-research connectors use SSE, URL ending
  in `/sse/`). No temporary tunnels for a public submission; the endpoint
  must stay reachable for review and domain verification.
- **Authentication**: OAuth 2.1 when user auth is needed; OAuth with client
  ID metadata documents is the recommended registration mechanism.
- **Developer mode**: custom connectors are added by enabling developer mode
  in ChatGPT settings and entering the server URL.
- **Deep research / company knowledge**: requires two read-only tools named
  `search` and `fetch` with the documented schemas. Servers without them work
  as full-MCP developer-mode connectors but not in deep research.
- **Broad distribution**: beyond a single workspace goes through the Apps
  SDK submission and review.

## What this project has today

- A production-quality MCP server in the CLI (unreleased; the first release
  shipping `melange mcp` is still pending — see gap 11): `melange mcp` — 18
  tools on stdio, 17 over Streamable HTTP (`upload_model` is stdio-only).
  See `internal/cmd/mcp/mcp.go` and the catalog in `llms.txt`.
- **Tool annotations**: every tool sets `ReadOnlyHint`, `DestructiveHint`,
  `IdempotentHint`, and `OpenWorldHint` (`internal/mcp/tool_*.go`; the write
  scope is derived from `ReadOnlyHint` in `internal/mcp/scopes.go`). No tool
  sets a `title` yet.
- **HTTP transport**: stateless Streamable HTTP with per-request
  `Authorization: Bearer` (PAT `ztp_` or OAuth `zoa_` token), `GET /healthz`
  liveness, Origin allow-listing, optional token validation, and — with
  `--resource` — OAuth protected-resource semantics: audience enforcement
  plus RFC 9728 discovery metadata at
  `/.well-known/oauth-protected-resource`, pointing at the Melange API host
  as the authorization server (`internal/mcp/httpserver/metadata.go`).
  Read-only OAuth tokens get RFC 6750 `insufficient_scope` challenges on
  write tools. The server itself speaks plain HTTP; TLS termination is the
  deployment's job.
- **Distribution**: Homebrew cask `zetic-ai/tap/melange`, npm
  `@zetic-ai/melange-cli`, `script/install.sh`, GitHub releases.
- **Docs**: README (client setup for Claude Code, Claude Desktop, Cursor),
  `llms.txt` (full tool catalog and transport contract), generated command
  reference under `docs/reference/`.
- **Registry manifest**: `server.json` at the repo root, valid against the
  2025-12-11 registry schema, and `mcpName: "ai.zetic/melange"` in
  `npm/package.json` (the registry's npm ownership marker).
- **Not present**: a hosted public HTTPS deployment of the server, MCPB
  packaging, `search`/`fetch` tools, a connector-specific privacy policy
  URL, or any submission.

## Gap list before a submission could be made

### Both directories

1. **Hosted endpoint**: stand up a stable public HTTPS deployment of
   `melange mcp --transport http` (TLS terminated in front of it) with
   `--resource` set to its canonical URL, and keep it up through review.
2. **OAuth client registration**: verify the Melange API's authorization
   server supports what the clients use — dynamic client registration or
   client ID metadata documents (or, for Anthropic, a static client ID held
   by Anthropic). The AS lives in the Melange API, not this repo, so this is
   unverified today and must be confirmed end to end from claude.ai and
   ChatGPT before submitting.

### Anthropic Connectors Directory

3. **Tool titles**: add a `title` to every tool (the hints already exist).
4. **Org access**: a Claude Team/Enterprise organization with
   directory-management access to reach the submission portal.
5. **Listing collateral**: documentation URL, privacy policy URL (covering
   collection, usage/storage, sharing, retention, contact), support
   contact, icon, tagline, description, categories, and a reviewer test
   account with credentials.
6. Optional local-distribution path: package the stdio server as an MCPB
   desktop extension and use the separate desktop-extension form.

### ChatGPT

7. **Developer-mode validation**: connect the hosted endpoint as a custom
   connector in a workspace and exercise the catalog (help-center specifics
   were not fetchable — verify the current flow when doing this).
8. **Deep research (optional)**: only if wanted, add read-only `search` and
   `fetch` tools with OpenAI's documented schemas; without them the server
   is a full-MCP connector only.
9. **Broad distribution (optional)**: Apps SDK submission with domain
   verification.

### MCP registry (`server.json`) — manual, gated publication

10. **Namespace authentication**: prove ownership of `zetic.ai` via the
    registry's DNS authentication to publish under `ai.zetic/*`.
11. **Version alignment**: `server.json` carries the placeholder version
    `0.4.0`; before publishing, set it (and the npm package reference) to
    the first released version that actually ships `melange mcp`.
12. **npm release**: the ownership check reads `mcpName` from the package
    published on npmjs.org, so the next npm release must include the
    `mcpName` field added here before `mcp-publisher publish` can succeed.
    Publication remains a manual, gated action like brew/npm — no CI step
    publishes the registry entry.
