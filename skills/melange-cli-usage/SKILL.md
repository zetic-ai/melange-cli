---
name: melange-cli-usage
description: "Use when interacting with zetic.ai Melange: uploading/deploying on-device AI models, browsing repos/reports via the melange CLI. Covers auth, JSON output contract, exit-code branching, upload/resume workflows, and the raw API escape hatch."
---

# Using the melange CLI

`melange` is the CLI for the Zetic.ai Melange platform (on-device AI model
deployment and benchmarking). It is agent-first: data goes to stdout,
progress/diagnostics go to stderr, exit codes are stable, and `--json`
output is machine-exact. Run `melange help environment`, `melange help
exit-codes`, and `melange help formatting` for the authoritative topics.

## Authentication

Prefer the environment variable — it overrides every stored credential:

```sh
export MELANGE_API_KEY=ztp_...          # personal access token
melange auth status --json              # verify: exit 0 = authenticated
```

Exit 4 from `auth status` means not logged in or token rejected.
Alternatives: `MELANGE_API_KEY_FILE=/path/to/token` (fails loudly if
unreadable), or a stored login for interactive setups:
`melange auth login --with-token < token.txt`. `melange auth token`
prints the resolved token (stdout has nothing else). Set `MELANGE_HOST`
to target a non-default API host. Pass `--no-input` to guarantee no
interactive prompts.

## Exit codes — branch on these

| Code | Meaning | Agent action |
|------|---------|--------------|
| 0 | Success | Continue |
| 1 | API/network/command failure | Possibly transient; the CLI already retried idempotent requests |
| 2 | Usage error (bad flags/args) | Bug in your invocation — fix the command, do not retry |
| 4 | Auth error (no/rejected token) | Fix credentials, do not retry |
| 130 | Interrupted (Ctrl-C) | Cancellation; an interrupted upload keeps its session |

## Output contract

- `--json`: for server-backed commands the payload is the API response
  byte-for-byte (field names, order, unknown fields preserved).
- `--jq EXPR` (implies `--json`): jq filter; filtered values are
  re-marshaled with sorted object keys; bare strings print raw.
- `--template TMPL` (implies `--json`): Go template with `tablerow`,
  `timeago`, `json` functions.
- Never parse TTY tables. On a TTY humans get aligned tables; when
  stdout is not a TTY you get headerless tab-separated values — but for
  agents, always prefer `--json`/`--jq`.
- List commands emit the page envelope `{"results": [...], "count": N}`
  exactly as returned. `--paginate` (alias `--all`) merges all pages
  into one envelope (top-level keys then re-marshaled in sorted order).

## Core workflow: create repo → upload → wait → status

```sh
# 1. Create a repository (in the account behind your token)
melange repo create whisper-tiny --private --json --jq .full_name

# 2. Preview the upload manifest first (no network calls)
melange model upload -R acme/whisper-tiny model.onnx --input audio.bin --dry-run

# 3. Upload and wait for conversion (--input order defines input_index)
melange model upload -R acme/whisper-tiny model.onnx \
  --input audio.bin --input mask.bin --wait --json

# 4. Check conversion later (MODEL_KEY like m_ab12cd)
melange model status m_ab12cd -R acme/whisper-tiny --jq .state
```

- `-R ACCOUNT/REPO` is **required** on every `melange model` command —
  uploads never fall back to a default repository.
- `--wait` polls until a terminal state (default `--timeout 30m`);
  under `--wait`, exit 1 means conversion failed or the timeout elapsed.
  A plain `model status` read is a query and exits 0 regardless of state.
- Browse repositories: `melange repo list --search whisper --paginate`,
  `melange repo list --jq '.results[].full_name'`,
  `melange repo view acme/whisper-tiny --json`.

## Resume after interruption

Interrupting an upload (exit 130) preserves the session server-side, and
the CLI prints the exact resume command on stderr, e.g.:

```
Resume with: melange model upload --resume up_ab12cd -R acme/whisper-tiny
```

Run that printed line verbatim — already-uploaded bytes are never
re-sent. Housekeeping: `melange model upload --sessions -R acme/repo`
lists sessions; `--cancel SESSION_ID` discards one.

## Escape hatch: `melange api`

Call any `/v1` endpoint not yet wrapped by a dedicated command. Same
transport as other commands: stored credentials, automatic retries for
idempotent requests (GET/HEAD or anything with an `Idempotency-Key`
header), standard error envelopes.

```sh
melange api /v1/me --jq .account.name          # GET, extract one value
melange api /v1/repos -F name=demo -F is_private=true   # fields => POST
echo '{"name":"demo"}' | melange api /v1/repos --input -  # raw body
melange api /v1/models -F name=demo -H 'Idempotency-Key: 0698c9b1'
melange api -X GET /v1/repos -f search=whisper --include  # query params
```

Paths must be relative to the configured host (absolute URLs are
rejected — credentials never leave the configured host). `-f` adds a
string field, `-F` a typed field (`true`/`false`/`null`/ints; `@path`
inserts file contents; `key[sub]=v` nests; `key[]=v` appends arrays).
Fields switch the method to POST unless you pass `-X GET` (then they
become query parameters). Non-2xx bodies still print to stdout with a
one-line summary on stderr; pagination and polling are your job here.

## Pitfalls

- `-R ACCOUNT/REPO` is required on all `melange model` commands; there
  is no default repo.
- Run uploads with `--dry-run` first to validate the manifest cheaply.
- Never parse TTY tables — use `--json` (byte-exact) or `--jq`.
- Exit 130 means the upload session is preserved; resume with the
  printed `--resume` line instead of restarting the upload.
- Exit 2 = your invocation is wrong (don't retry); exit 4 = credentials
  (don't retry until fixed).
- `--jq` output re-marshals (sorted keys); use plain `--json` when you
  need the byte-exact server payload.
- `MELANGE_DEBUG=1` logs request/response lines to stderr (tokens and
  headers are never logged).
