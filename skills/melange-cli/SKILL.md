---
name: melange-cli
description: "Use when running the zetic.ai `melange` CLI: uploading/importing on-device AI models, monitoring conversion status, reporting Melange benchmarks, generating deployment guides, browsing repos and the public library. Covers auth, choosing between importing a Hugging Face repo id and uploading exported artifacts, JSON output contract, exit-code branching, upload/resume, non-blocking conversion monitoring, model report templates, and the raw API escape hatch. Also points at the `melange mcp` MCP server alternative and when to prefer its tools over shelling out. Do NOT trigger for general ML work — training, local inference, or other model registries — unless the `melange` CLI is involved."
---

# Melange CLI

`melange` is the CLI for the Zetic.ai Melange platform (on-device AI model
deployment and benchmarking). It is agent-first: data goes to stdout,
progress/diagnostics go to stderr, exit codes are stable, and `--json`
output is machine-exact except for documented composed or redacted results.
Waited upload/import results compose model identity with final status, and
download authorization URLs are redacted credentials. Run `melange help
exit-codes`, and `melange help formatting` for the authoritative topics.

## MCP server alternative

`melange mcp` serves these same operations as MCP tools (stdio by default;
`--transport http` for shared remote deployments). When the Melange MCP server
is already connected to your session, prefer its tools over shelling out for
anything in its catalog. Use the CLI when the operation has no tool (auth
management, `model download` to disk, bucketed `.pt2` or `--input-manifest`
uploads, `melange api`), or when the server is connected over HTTP and the
work needs the user's local files — the `upload_model` tool exists only on
stdio. The authoritative tool reference — transports, per-transport auth,
confirm-gated tools, and the write-scope refusal — is the `## melange mcp`
section of the CLI repository's `llms.txt`, plus `melange mcp --help`; this
skill deliberately does not restate it. Every rule below (non-blocking
conversion monitoring, the pipeline panel, the report templates) applies
unchanged when the data came from MCP tools.

## Authentication

Browser OAuth is the default for interactive use — `melange auth login` opens a browser (PKCE loopback, `zoa_`/`zor_`, auto-refreshed) and falls back to PAT paste when browser/display unavailable:

```sh
melange auth login                          # OAuth recommended (browser)
melange auth login --with-token < token.txt # PAT for CI/headless (ztp_...)
export MELANGE_API_KEY=ztp_...              # PAT env var also overrides stored OAuth
melange auth status --json                  # verify: exit 0 = authenticated
```

Exit 4 from `auth status` means not logged in or token rejected. When the
server supports it, `auth status` also shows the account's plan; `melange plan`
reports it in detail.
Alternatives: `MELANGE_API_KEY_FILE=/path/to/token` (fails loudly if
unreadable; empty file is ignored for OAuth), `melange auth login --no-browser`
(prints URL for SSH), or `--insecure-storage` to store in 0600 `config.yml`.
`melange auth token` prints the resolved token (stdout has nothing else).
Set `MELANGE_HOST` to target a non-default API host. Pass `--no-input` to guarantee no
interactive prompts.

Credential precedence is `MELANGE_API_KEY` > `MELANGE_API_KEY_FILE` (non-empty) > OAuth (keyring/config, auto-refreshed, `zoa_`/`zor_`) > PAT keyring > legacy config fallback.

## Exit codes — branch on these

| Code | Meaning | Agent action |
|------|---------|--------------|
| 0 | Success | Continue |
| 1 | API/network/command failure | Possibly transient; the CLI already retried idempotent requests |
| 2 | Usage error (bad flags/args) | Bug in your invocation — fix the command, do not retry |
| 4 | Auth error (no/rejected token) | Fix credentials, do not retry |
| 130 | Interrupted (Ctrl-C) | Cancellation; an interrupted upload keeps its session |

## Output contract

- `--json`: for server-backed commands the payload preserves the API response
  bytes (field names, order, unknown fields preserved) except for normalizing
  the terminator to exactly one trailing newline. The documented exceptions are:
  `model upload/import --wait` returns the documented
  `{"model": ..., "status": ...}` composite, and `model download`
  replaces signed artifact URLs with `"<redacted>"`.
- `--jq EXPR` (implies `--json`): jq filter; filtered values are
  re-marshaled with sorted object keys; bare strings print raw.
- `--template TMPL` (implies `--json`): Go template with `tablerow`,
  `timeago`, `json` functions.
- Read data through `--json`/`--jq`. On a TTY humans get aligned tables under a
  ruled header with a trailing row count (`--format table` forces that layout
  when piping to a pager); when
  stdout is not a TTY you get headerless tab-separated values. Backslash,
  tab, carriage return, and newline inside cells are escaped as `\\`, `\t`,
  `\r`, and `\n`; for agents, always prefer `--json`/`--jq`.
- List commands emit the page envelope `{"results": [...], "count": N}`
  with the same response-byte contract. `--paginate` (alias `--all`) merges
  all pages into one envelope (top-level keys then re-marshaled in sorted order).

## Identifiers — do not interchange these

| Identifier | What it is | How to get it |
|------------|------------|---------------|
| `ACCOUNT/REPO` | A Melange repository address | `melange repo list` |
| `MODEL_KEY` | One converted model version in a repository | `melange model list -R ACCOUNT/REPO` |
| `TARGET_ID` | One downloadable converted artifact; opaque `tm_…`/`ltm_…` | `melange model targets MODEL_KEY -R ACCOUNT/REPO` |

They chain in that order: repo → model key → target id. `ACCOUNT/NAME` in
`melange library view` addresses a **public library** model and is not a
`MODEL_KEY`. Never substitute a repository name, a display name, or a Hugging
Face id for `MODEL_KEY` or `TARGET_ID`, and never parse a `TARGET_ID`.

## Hugging Face models: import or artifacts

LLMs import straight from the repo id. General models ship as exported
artifacts. Model type is fixed at `repo create`, so classify before creating
the repository:

```sh
curl -fsSL "https://huggingface.co/api/models/OWNER/NAME" | jq -r .pipeline_tag
```

`text-generation` → import. Everything else (vision, speech, embeddings,
detection) → artifacts. Ask the user when the tag is missing or ambiguous.

```sh
# LLM — import the repo id into an llm repository
llm_repo="$(melange repo create llama-1b --private --model-type llm --jq .full_name)"
melange model import meta-llama/Llama-3.2-1B -R "$llm_repo" --json

# General — export locally, then upload the artifacts to a general repository
gen_repo="$(melange repo create vit-base --private --jq .full_name)"
python export.py   # torch.export.save -> model.pt2; np.save -> input_0.npy
melange model upload -R "$gen_repo" model.pt2 --input input_0.npy --dry-run --json
```

Before the first export step, tell the user what the artifact path involves: a
local export to `.pt2` plus `.npy` sample inputs, which downloads the weights,
needs PyTorch >= 2.9, and fixes the input shape the deployed model will accept.
Then proceed. See <https://docs.zetic.ai/model-preparation/pytorch-export> for
the export snippet and the graph constraints it has to satisfy.

## Core workflow: create repo → upload → wait → status

```sh
# 1. Create a repository (in the account behind your token)
repo="$(melange repo create whisper-tiny --private --jq .full_name)"

# 2. Preview the upload manifest first (no network calls)
melange model upload -R "$repo" model.onnx --input audio.bin --dry-run --json

# 3. Check the upload entitlement immediately before uploading.
#    `remaining` is what the server will actually allow now (spike headroom
#    included; null = unlimited). Preflight on it, not on limit-used.
upload_remaining="$(melange usage quotas --jq '.model_uploads.remaining // "unlimited"')" || exit $?
[ "$upload_remaining" != "0" ] || { echo "No model-upload quota remaining for this account" >&2; exit 1; }

# 4. Upload and wait once; preserve both identity and terminal status
upload_json="$(melange model upload -R "$repo" model.onnx \
  --input audio.bin --wait --json)" || exit $?
model_key="$(printf '%s\n' "$upload_json" | jq -er .model.key)" || exit $?
model_state="$(printf '%s\n' "$upload_json" | jq -er '.status.state')" || exit $?
test "$model_state" = ready || exit 1

# 5. Use the captured identifiers in later structured commands
melange model view "$model_key" -R "$repo" --json
```

For bucketed `.pt2` models, declare buckets in the same order as their input
groups. Each bucket must have the same number of inputs; within a group, order
defines `input_index`. Validate the mapping without a network call first:

```sh
melange model upload -R acme/vision model.pt2 \
  --bucket 0:1x3x224x224 --input image224.npy --input mask224.npy \
  --bucket 1:1x3x384x384 --input image384.npy --input mask384.npy \
  --dry-run --json
```

Sample inputs are optional. This is a valid model-only preflight:
`melange model upload -R acme/repo model.onnx --dry-run`.

Expired upload URL reissue intentionally mints fresh signed URLs and sends no
`Idempotency-Key`; create, complete, and cancel keep their replay keys.

For advanced workflows, `--input-manifest` uses a CLI-private local-file
document, not the public API wire manifest. Each `path` is resolved relative
to the manifest file, so the document is independent of the process CWD:

```json
{
  "manifest_version": 2,
  "files": [
    {"path": "models/model.onnx", "role": "model"},
    {"path": "samples/audio.npy", "role": "input", "input_index": 0}
  ]
}
```

`path` and `role` are required. `filename` is optional. For input files,
`input_index` is optional and defaults by order. Bucketed manifests add
`options.buckets` entries shaped as
`{"index": 0, "dims": [1, 3, 224, 224]}` and a matching `bucket_index`
on each input. Then run:

```sh
melange model upload -R acme/whisper-tiny \
  --input-manifest ./upload/manifest.json --dry-run --json
```

- `-R ACCOUNT/REPO` is **required** on every `melange model` command —
  uploads never fall back to a default repository.
- `--wait` polls until a terminal state (default `--timeout 30m`);
  under `--wait`, exit 1 means conversion failed or the timeout elapsed.
  A plain `model status` read is a query and exits 0 regardless of state.
  The workflow above is written for scripts. When a person is waiting, drop
  `--wait` and monitor in the background instead — see "Presenting results
  to the user".
- Browse repositories: `melange repo list --search whisper --paginate`,
  `melange repo list --jq '.results[].full_name'`,
  `melange repo view acme/whisper-tiny --json`.

## Browse models and targets

The runtime engine that produced an artifact is not part of the public API.
`melange model targets` reports `precision`, `quant_type` and
`compatibility.ap_types` (cpu/gpu/npu) instead, and report records carry an
opaque `variant` token in place of the engine. Never parse a `variant`, and
never describe a target by an engine name — you do not have one.

Once a model exists you can inspect it and its converted targets:

```sh
model_key=$(melange model list -R acme/whisper-tiny --jq '.results[] | select(.is_default) | .key')
melange model view "$model_key" -R acme/whisper-tiny --jq .download_ready
melange deploy guide "$model_key" -R acme/whisper-tiny --language android-kotlin --mode auto
melange model targets "$model_key" -R acme/whisper-tiny --json      # converted targets
melange model set-default "$model_key" -R acme/whisper-tiny         # pin the repo default
melange model import meta-llama/Llama-3.2-1B -R acme/llm --json  # interactive: returns at once, state converting
melange model import meta-llama/Llama-3.2-1B -R acme/llm --wait  # scripts: block until ready or failed
target_id=$(melange model targets "$model_key" -R acme/whisper-tiny --jq '.results[0].target_id')
melange model download "$model_key" -R acme/whisper-tiny --target "$target_id" --output ./models --yes
```

Waited upload and import output has one stable shape:
`{"model": <create/import response>, "status": <final ModelStatusResponse>}`.
Use `.model.key` for identity and `.status.state` for the terminal outcome.
Without `--wait`, each command keeps its raw create/import response.

`model download` is **billable** and replay-safe across CLI processes — a retry
after an interruption reuses the charge rather than adding one. Pass `--yes`;
without it the command prompts. `--output` refuses to overwrite without
`--force`, and a collision discovered after authorization asks for `--force`
without charging again. Use `--output -` for a single artifact only, never
alongside structured-output flags. Transient artifact GETs retry on their own;
30 seconds without byte progress triggers a retry, and each chunk resets the
timer. Signed URLs and access tokens are never stored.

## Read benchmark reports: `melange report view`

`report view MODEL_KEY -R ACCOUNT/REPO` reads a model's benchmark report.
`--type general|llm|package` selects the report; without it the CLI probes
general → llm → package and shows the first that exists (exit 1 "no report
available" when none do).

```sh
model_key=$(melange model list -R acme/whisper-tiny --jq '.results[] | select(.is_default) | .key')

# The dashboard table on a TTY; --mode auto|speed|accuracy picks the cell value
melange report view "$model_key" -R acme/whisper-tiny --mode accuracy

# Agents: take the raw records, not the derived table
melange report view "$model_key" -R acme/whisper-tiny --json \
  --jq '[.records[] | select(.ap_type=="npu" and .metric=="latency_ms")]
        | group_by(.device.marketing_name)[]
        | {device: .[0].device.marketing_name, best: (map(.value)|min)}'
```

The TTY table re-derives the dashboard's mode picks from the raw records
(speed = lowest latency; accuracy = highest SNR, ties to lower latency;
auto = fastest run whose SNR exceeds 20 dB, else speed). Non-TTY output is
**one raw record per line** (flat measurement fields) — scripts get the
measurements, not the derived table. Always use `--json` for the exact
response.

A report covers the whole model across every device and precision the account
can see. Filtering to one device is a slice, not the report — see "Report a
model" below.

## Presenting results to the user

These rules govern the answer you write for a human, not how you call the CLI.
They apply whenever a person is waiting on you; non-interactive scripts keep
using the blocking forms.

### Never block on conversion

Conversion runs server-side for minutes. `model import` and `model upload`
**without** `--wait` return as soon as the model is registered — state
`converting`. With `--wait` they block until `ready` or `failed`. Blocking in
the foreground leaves the user watching a stalled command with no idea which
phase is running.

So: import or upload **without** `--wait`, answer immediately with the pipeline
panel and what remains, then poll in the background.

The public conversion state machine has exactly four lowercase states —
`converting` → `optimizing` → `ready`, or `failed`. `stage` is `convert` while
converting and `benchmark` while optimizing. `download_ready` turns true at
`optimizing`, so the model is already usable while benchmarking finishes. There
is no percentage: `progress` is always null, so never invent one. `retry_after`
is the server's suggested poll interval (5 seconds while non-terminal). These
are not the UPPERCASE upload-session states — do not mix the two vocabularies.

Run these two stages in the background, in order:

```sh
# Stage 1 — returns the moment conversion finishes (~30m ceiling).
i=0
while [ "$i" -lt 360 ]; do
  state="$(melange model status "$model_key" -R "$repo" --jq .state)" || exit $?
  [ "$state" = converting ] || break
  i=$((i + 1)); sleep 5
done
melange model status "$model_key" -R "$repo" --json
```

Report that transition, then hand the rest to the CLI's own waiter:

```sh
# Stage 2 — exit 0 ready, 1 failed or timed out.
melange model status "$model_key" -R "$repo" --wait --timeout 30m --json
```

Stage 1 returns immediately when the model is already past `converting`, so it
is also the correct first step for a conversion that started earlier.

### Pipeline panel

Render the panel after every import, upload, and status check, filled from
`.state`. Glyphs are `✔` complete, `◐` in progress, `○` pending, `✖` failed.

```
╭─ Conversion Pipeline ───────────────────────────────╮
│   ✔ CONVERTING   →   ◐ OPTIMIZING   →   ○ READY     │
│     complete           in progress       pending    │
╰─────────────────────────────────────────────────────╯
```

Substitute the glyph and caption per phase: reached phases `✔ complete`, the
current one `◐ in progress`, later ones `○ pending`, `ready` once reached
`✔ available`, and on failure `✖ failed` with the phases after it `○ blocked`.

State the phase in words next to the panel: at `converting`, benchmarking and
packaging are still to come; at `optimizing`, say the model is already
downloadable while benchmarks finish; at `ready`, point to the report and the
deployment guide. On `failed`, mark the failing stage from `.stage`, quote
`.failure_code` verbatim, and do not guess a cause the code does not name.

### Report a model

Fill in a template and print the result in your reply:

| Model type | Template |
|------------|----------|
| LLM | `assets/report-llm.md` |
| everything else | `assets/report-general.md` |

Read the template before you answer and keep its section order. It carries the
verified jq for every figure, the rounding and missing-value conventions, and the
path for reporting a single device when the user names one.

The dashboard's deployability percentage is a web-UI threshold widget with no API
field behind it. Say it is unavailable rather than producing a number for it.

## Browse the public library and your usage

```sh
melange library list --task vision --provider Zetic --jq '.results[].full_name'
melange library view zetic/whisper-tiny --json     # includes the full readme
melange library providers --jq '.results[] | select(.model_count>=10) | .name'

melange usage --jq .prompts                        # this period's counters
melange usage quotas --jq .model_uploads.remaining # headroom now; null = unlimited
melange usage quotas --jq .credits                 # advisory credit balance
melange plan --jq .plan                            # legacy tier: free|lite|pro|pro_plus|enterprise
melange plan --jq .tier                            # pricing identity: free|pro|team|enterprise; null = legacy billing
```

`library list` filters map to query params (`--task` repeats; a model
matching ANY task is included). Its `--search` is case- and
separator-insensitive across both `name` and `full_name`; hyphens,
underscores, slashes, and spaces are ignored. `usage quotas` renders each
quota as `used/limit (pct%)`, or `unlimited` when the limit is null; its
`--json`/`--jq` output also carries `remaining` per counter — the amount the
server will actually allow now (spike headroom included, floored at 0; null =
unlimited). Prefer `remaining` over `limit - used` for preflight.

`melange plan` reports two vocabularies. `plan` is the legacy tier quotas
derive from (`free|lite|pro|pro_plus|enterprise`) — what the server enforces,
matching the dashboard; an account that bypasses quota limits reports
`pro_plus` with unlimited quotas. `tier` is the current pricing identity
(`free|pro|team|enterprise`), null on accounts still on legacy billing;
`billing_generation` (`legacy|v3`) says which system governs.
`max_model_bytes` preflights only the plan's own model-size entitlement —
credits and debt are checked separately at conversion time. A null
`max_model_bytes` means a custom contract, **not** an unlimited one (unlike a
null quota limit): the credit ledger still refuses runs above the self-service
size ceiling.

### Entitlement disclosure (required)

Before uploading a model or describing benchmark coverage as complete, inspect
`melange usage quotas --json`. If `model_uploads.remaining == 0`, do not attempt
or retry an upload: tell the user they have no upload quota left under their
current plan, not that the CLI is broken. Report records contain only devices
visible to the authenticated account; never call them the full benchmark unless
completeness is established.

Plan identity is knowable — read it from `melange plan --jq .plan` (and
`.tier` for the pricing identity; null there means legacy billing). When
`model_uploads.remaining == 0` and the plan is **Free** (or **Lite**), say that
the plan is why model upload is locked; on other plans say the monthly upload
quota is exhausted. Do not attribute a zero to the wrong cause: check `melange
plan` rather than inferring Free from a zero limit, a filtered report, or an
HTTP error.

Credits gate conversions and are ADVISORY: preflight with
`melange usage quotas --jq '{credits: .credits.available, debt: .credits.outstanding_debt}'`.
`.credits.available > 0` AND `.credits.outstanding_debt == 0` are necessary
but not sufficient — the per-conversion charge grows with model size, so a
positive balance can still refuse a large model. Branch on the machine
`error.code`, not on the status: HTTP 402 `billing_error` is
`credit_balance_exhausted` (top up, nothing was charged) or
`subscription_past_due` (fix the payment method); HTTP 409 `conflict_error` is
`credit_debt_outstanding` (settle the debt on the dashboard); HTTP 413
`request_too_large` is `custom_model_too_large` (over the plan's own
model-size entitlement — check `melange plan`) or `credit_model_too_large`
(over the self-service size ceiling, which no credit balance buys — contact
support). A refused upload completion leaves the session parked and resumable
— after remediation, replay it:
`melange model upload --resume SESSION_ID -R ACCOUNT/REPO`.

Library `ACCOUNT/NAME` values identify public repositories, not converted model
keys. Inspect their existing models and reports directly; this is a read-only
path and needs no import or upload:

```sh
repo=$(melange library list --search QUERY --jq '.results[0].full_name')
key=$(melange model list -R "$repo" --jq '.results | (map(select(.is_default and .state=="ready")) + map(select(.state=="ready")))[0].key // empty')
[ -n "$key" ] || { echo "No ready model is available in $repo" >&2; exit 1; }
melange report view "$key" -R "$repo" --json
```

Reading a library model's public benchmarks needs no import or upload — the path
above is the whole job.

## Get exact deployment code

Use the selected model key—not a Hugging Face ID—to request deployment code.
The supported languages are `android-kotlin`, `android-java`, `ios-swift`, and
`flutter`; inference modes are `auto`, `speed`, and `accuracy`.

```sh
melange deploy options --json
repo=shinilheo/LFM2.5_350M
key=$(melange model list -R "$repo" --jq '.results | (map(select(.is_default and .state=="ready")) + map(select(.state=="ready")))[0].key // empty')
[ -n "$key" ] || { echo "No ready model is available in $repo" >&2; exit 1; }
melange deploy guide "$key" -R "$repo" --language android-kotlin --mode auto
melange deploy guide "$key" -R "$repo" --language ios-swift --mode speed --json
```

The plain guide is ordered Markdown with copyable SDK install and inference
snippets; `--json` is the structured response. Repository coordinate, version,
family, mode, and SDK language are already resolved. General-model tensor
creation is left an explicit TODO — shapes and preprocessing are model-specific,
so inspect the tensor I/O metadata rather than inventing inputs. Ship the guide's
own SDK fields and callbacks; do not add ones it does not name.

Every guide uses `YOUR_PERSONAL_KEY`. The command never interpolates, prints,
or persists the active PAT. Do not replace the placeholder in chat, logs,
generated JSON, or shell-history suggestions.

## Resume after interruption

Interrupting an upload (exit 130) preserves the session server-side, and
the CLI prints the exact resume command on stderr. If that output is no longer
available, derive the opaque ID from the session list:

```sh
session_id=$(melange model upload --sessions -R acme/whisper-tiny --jq '.results | map(select(.state=="CREATED" or .state=="UPLOADING")) | first | .id // empty')
[ -n "$session_id" ] || { echo "No resumable upload session found" >&2; exit 1; }
melange model upload --resume "$session_id" -R acme/whisper-tiny
```

Prefer the exact line printed at interruption time — already-uploaded bytes are
never re-sent. Housekeeping: `melange model upload --sessions -R acme/repo`
lists sessions. Cancellation prompts for the exact session ID on a terminal;
agents and other non-interactive callers must pass
`--cancel SESSION_ID --yes`.

## Escape hatch: `melange api`

Call any `/v1` endpoint not yet wrapped by a dedicated command. Same
transport as other commands: stored credentials, automatic retries for
GET/HEAD/PUT and anything with an `Idempotency-Key` header, standard error
envelopes. Raw PATCH requests need an `Idempotency-Key` to be retried.

```sh
melange api /v1/me --jq .account.name          # GET, extract one value
melange api /v1/repos -F name=demo -F is_private=true   # fields => POST
echo '{"name":"demo"}' | melange api /v1/repos --input -  # raw body
melange api /v1/models -F name=demo -H 'Idempotency-Key: 0698c9b1'
melange api -X GET /v1/repos -f search=whisper --include  # query params
```

Paths must be relative to the configured host and resolve under `/v1`
(absolute URLs and anything outside `/v1` are rejected with exit 2 —
credentials never leave the configured host or its public API). `-f` adds a
string field, `-F` a typed field (`true`/`false`/`null`/ints; `@path`
inserts file contents; `key[sub]=v` nests; `key[]=v` appends arrays).
Fields switch the method to POST unless you pass `-X GET` (then they
become query parameters). Non-2xx bodies still print to stdout with a
one-line summary on stderr; pagination and polling are your job here.

## Pitfalls

- Run uploads with `--dry-run` first to validate the manifest cheaply.
- `MELANGE_DEBUG=1` logs request/response lines to stderr (tokens and
  headers are never logged).
- `MELANGE_API_TIMEOUT` bounds ordinary API requests (default `30s`);
  upload inactivity and conversion `--wait` have separate timeout flags.
- Successful `melange api` output is committed only after the complete body
  is read. Increase `MELANGE_API_TIMEOUT` for legitimately slow or large
  responses; a failed read leaves stdout empty instead of emitting partial data.
- Never run `--wait` in the foreground while a person is waiting; import or
  upload without it, report the phase, and monitor in the background.
- Never present a metric the response did not carry. Render it as `-` in a table
  and `N/A` in a card so the gap stays visible, and never compare values measured
  on different accelerators as if one were a speedup.
- Report the whole model, not one device: summary values are bests across all
  devices, so labelling them with a device name misattributes the measurement.
  Print the report in your reply rather than saving it to a file.
