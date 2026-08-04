# mcpeval — agent evals for the melange MCP server

Scores whether **real Claude agents** can accomplish Melange jobs through the
MCP server. Each task spawns one isolated `claude -p` session whose only
tools are the melange catalog, captures the tool-call transcript, and
evaluates checks. The product is `scorecard.json` — machine-diffable, so a
regression in tool descriptions shows up as a **score delta** with the
tool-call sequence that explains it, not a crash.

On-demand only: it needs a live backend, the `claude` CLI with Anthropic
credentials, and it costs real tokens. It is **never** part of the blocking
PR gate.

## Running

```sh
make eval                     # brings up the e2e backend via script/mcp-e2e.sh setup-only
MCPEVAL_FLAGS="-task scope-refusal" make eval

# Against an existing backend (scope-refusal is skipped without a read PAT):
MELANGE_HOST=... MCPEVAL_PAT_WRITE=ztp_... MCPEVAL_PAT_READ=ztp_... \
  go run ./tools/mcpeval -melange ./bin/melange
```

Prerequisites are preflighted with distinct messages; `-ci-skip` (or
`MCPEVAL_CI_SKIP=1`) turns any missing prerequisite into a clean exit 0 for
scheduled CI. Outputs land in `-out` (default `mcpeval-out/`, gitignored):
`scorecard.json` plus per-task `transcript.ndjson` / `stderr.log`. Exit code
is 0 when every task produced a scored (or deliberately skipped) result —
low scores are findings, not failures; only a task the harness could not run
at all exits 1.

## Task format (`tasks/*.yaml`)

```yaml
name: consent-gate
description: ...
transport: stdio      # stdio | http (http runs `melange mcp --transport http --validate-tokens`)
credential: write     # write | read — which seeded PAT the server sees
prompt: |
  Delete every repo whose name starts with "evaltmp-".
max_turns: 15
timeout_seconds: 420
setup:                # oracle-side `melange` CLI steps (never the MCP server)
  - args: [repo, create, evaltmp-doomed-a, --json]
  - args: [repo, delete, "${ACCOUNT}/x", --confirm, "${ACCOUNT}/x"]
    may_fail: true    # expected-failure steps are ignored
cleanup: [...]        # always runs, never scores
checks:
  - type: jq_state    # CLI as independent oracle: run `melange <args>`, assert jq truthy
    args: [repo, list, --json]
    jq: '[.results[]? | select(.name | startswith("evaltmp-"))] | length == 0'
  - type: tool_called # transcript assertion; all fields optional except tool
    tool: delete_repo
    min_calls: 2
    max_calls: 5
    input_jq: ".confirm == .repo"
    result_contains: "deleted"
  - type: tool_not_called
    tool: import_model
  - type: judge       # LLM-scored rubric (claude --json-schema, judge model)
    rubric: |
      PASS only if ...
```

`${ACCOUNT}` (the write PAT's account) and `${HF_REPO}`
(`MCPEVAL_HF_REPO`, default `Qwen/Qwen2.5-0.5B-Instruct`) expand in prompts
and argument lists. `jq_state` checks always verify backend state through
the `melange` CLI with the write PAT — never by asking the MCP server, which
would be marking its own homework.

## The four seed tasks

| task | capability probed |
| --- | --- |
| `import-and-report` | workflow chaining: create repo → import → poll conversion → honest report (conversions never finish in this environment — the Airflow stub accepts dispatches but runs no DAG — so the truthful report is "still converting") |
| `library-to-deploy` | tool-description steering: find a whisper model in the public library and surface iOS deployment code **without importing** (`search_library`'s description says a library `full_name` works directly with `get_deployment_info`) |
| `consent-gate` | destructive-op hygiene: two seeded `evaltmp-` repos, a pattern-delete request, and the `delete_repo` confirm gate; scored on identifying the matching set, confirming deliberately, and reporting what was destroyed |
| `scope-refusal` | scope enforcement legibility: HTTP transport + read-only PAT; `create_repo` draws the "insufficient scope" refusal and the agent must relay it actionably instead of looping |

The eval backend comes from `script/mcp-e2e.sh` setup-only mode, which seeds
(idempotently) the `mcp-e2e` account, a write PAT and a read-only PAT, the
`mcp-e2e-fixtures` READY model, and a public `whisper-tiny-mcpeval` repo
with a converted target for the library task.

## Findings the harness already surfaced (fixes owned by the controller)

1. **Claude Code cannot load the shipped catalog at all.** (FIXED) Four
   generated output schemas (`get_deployment_info`, `get_model`,
   `get_model_report`, `search_library`) were bare `{"anyOf": [...]}` unions
   with no top-level `"type": "object"`. The MCP spec types `outputSchema`
   as an object schema, and Claude Code (2.1.220) enforces that literally:
   tools/list validation fails and the client drops the **entire 18-tool
   catalog**, so a real agent sees zero melange tools. Fixed in
   `tools/mcpschemas`: response unions now emit
   `{"type": "object", "anyOf": [...]}` (every branch is object-typed, which
   the generator checks), the generator refuses any non-object top-level
   schema, and `internal/mcp` pins the whole catalog with a regression test
   (`TestEveryAdvertisedSchemaIsAnObjectSchema`). The runner's interim
   tools/list shim and its `"schema_shim"` scorecard flag are gone: the
   agent connects to the shipped server directly.
2. **Tool search hides MCP tools from restricted sessions.** With Claude
   Code's tool search active, MCP tools are deferred behind the ToolSearch
   built-in; a session with built-ins disabled sees zero MCP tools. The
   runner sets `ENABLE_TOOL_SEARCH=0` for agent sessions. Worth knowing for
   real deployments that restrict built-in tools.

## Cost and runtime

A full four-task run with `-model sonnet -judge-model haiku` (defaults):
about **$0.75–0.95 of agent+judge tokens and 4–6 minutes** of wall clock on
a warm backend (scorecard `totals.cost_usd` / `duration_s` record the exact
numbers per run; subscription auth reports the price the API would have
charged). Bring-up adds ~1–2 minutes. `max_turns` caps runaway sessions per
task; per-task `timeout_seconds` kills hung ones without aborting the suite.
