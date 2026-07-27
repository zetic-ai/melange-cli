---
name: melange-cli
description: "Use when interacting with zetic.ai Melange: uploading/deploying on-device AI models, monitoring conversion status, reporting benchmarks, browsing repos/reports via the melange CLI. Covers auth, JSON output contract, exit-code branching, upload/resume workflows, non-blocking conversion monitoring, how to present model status and benchmark results, and the raw API escape hatch."
---

# Melange CLI

`melange` is the CLI for the Zetic.ai Melange platform (on-device AI model
deployment and benchmarking). It is agent-first: data goes to stdout,
progress/diagnostics go to stderr, exit codes are stable, and `--json`
output is machine-exact except for documented composed or redacted results.
Waited upload/import results compose model identity with final status, and
download authorization URLs are redacted credentials. Run `melange help
exit-codes`, and `melange help formatting` for the authoritative topics.

## Authentication

Prefer the environment variable — it overrides every stored credential:

```sh
export MELANGE_API_KEY=ztp_...          # personal access token
melange auth status --json              # verify: exit 0 = authenticated
```

Exit 4 from `auth status` means not logged in or token rejected. When the
server supports it, `auth status` also shows the account's plan; `melange plan`
reports it in detail.
Alternatives: `MELANGE_API_KEY_FILE=/path/to/token` (fails loudly if
unreadable), or a stored login for interactive setups:
`melange auth login --with-token < token.txt`. `melange auth token`
prints the resolved token (stdout has nothing else). Set `MELANGE_HOST`
to target a non-default API host. Pass `--no-input` to guarantee no
interactive prompts.

Credential precedence is `MELANGE_API_KEY` > `MELANGE_API_KEY_FILE` >
explicitly selected config storage > OS keyring > legacy config fallback.

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
- Never parse TTY tables. On a TTY humans get aligned tables; when
  stdout is not a TTY you get headerless tab-separated values. Backslash,
  tab, carriage return, and newline inside cells are escaped as `\\`, `\t`,
  `\r`, and `\n`; for agents, always prefer `--json`/`--jq`.
- List commands emit the page envelope `{"results": [...], "count": N}`
  with the same response-byte contract. `--paginate` (alias `--all`) merges
  all pages into one envelope (top-level keys then re-marshaled in sorted order).

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

`model download` is **billable** and replay-safe across CLI processes. It
persists a host/repository/model/target authorization key in per-user application state and
serializes processes with a lock. Local output is recovery metadata, not part
of the billing identity: correcting an impossible file/stdout shape to a
directory keeps the charged key. Durable completion/recovery state also keeps
a waiting process or its later retry from rotating that key after another
process succeeds. Signed URLs and access tokens are never stored. Transient
artifact GETs retry; 403/404 refresh once, and 30 seconds without byte progress
triggers a retry while each chunk resets the timer. `--output` never overwrites
without `--force`. Known collisions validate before authorization, but artifact
names arrive only in the billable response; a later collision keeps replay
state and asks for `--force` without another charge. Agents must pass `--yes`.
Use `--output -` only for one artifact and never with structured-output flags.

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
│   ◐ CONVERTING   →   ○ OPTIMIZING   →   ○ READY     │
│     in progress        pending           pending    │
╰─────────────────────────────────────────────────────╯

╭─ Conversion Pipeline ───────────────────────────────╮
│   ✔ CONVERTING   →   ◐ OPTIMIZING   →   ○ READY     │
│     complete           in progress       pending    │
╰─────────────────────────────────────────────────────╯

╭─ Conversion Pipeline ───────────────────────────────╮
│   ✔ CONVERTING   →   ✔ OPTIMIZING   →   ✔ READY     │
│     complete           complete          available  │
╰─────────────────────────────────────────────────────╯

╭─ Conversion Pipeline ───────────────────────────────╮
│   ✔ CONVERTING   →   ✖ OPTIMIZING   →   ○ READY     │
│     complete           failed            blocked    │
╰─────────────────────────────────────────────────────╯
```

State the phase in words next to the panel: at `converting`, benchmarking and
packaging are still to come; at `optimizing`, say the model is already
downloadable while benchmarks finish; at `ready`, point to the report and the
deployment guide. On `failed`, mark the failing stage from `.stage`, quote
`.failure_code` verbatim, and do not guess a cause the code does not name.

### Report a model

Lead every model explanation with this order — never start from an incidental
detail:

1. **Name** — the Hugging Face or repository name, `ACCOUNT/REPO`, the model
   key, and the current state.
2. **Description** — a short paragraph: what the model does, its family and
   size, where it came from.
3. **Headline metrics** — the four-card panel below.
4. **Benchmarks** — the full report, written out in your reply.

Write the report **in the answer itself**. Do not save it to a file and hand
back a path, and do not replace it with a prose summary; the user asked for the
numbers, so print them. Sections in order: identity (repo, key, version, state,
source); description; the headline-metrics panel; the model-wide measurement
table; accuracy or signal-quality results; coverage and caveats (the entitlement
disclosure below); and the exact commands the numbers came from. Only write a
file when the user asks for one.

Take values from `--json`, never from a TTY table. Summary fields arrive
rounded to 2 decimals; raw `records[].value` floats do not, so round them for
display the way the CLI does — 1 decimal in per-device tables, 2 in headline
figures. Round only for display: never restate a measurement at a precision the
response did not support, and never adjust a number to make a point.

### Report the whole model, not one device

The report is the model's, the way the dashboard's model page is. Cover every
quantization (LLM) and every device (general) the response carried. Never
truncate a table silently, and never drop a column because some cells are null —
print `—` for a missing cell and keep the row.

`.summary.quants` (LLM) and `.summary.<metric>.all` (general) are **model-wide
bests, taken across all devices**. They belong to no single device, so never
label them with one. This is the failure to avoid: quoting `.summary.quants`
under a heading like "Device: Google Pixel 10" states a measurement that device
never produced — TinyLlama's 116.51 TPS is an iPad Pro 12.9" GPU run.

Narrow to one device only when the user names one. Then say plainly that it is a
device slice, and derive it from `records[]` filtered on that
`device.marketing_name` — never by relabeling the summary.

```sh
# LLM: every quantization, model-wide. This is the report's main table.
melange report view "$model_key" -R "$repo" --type llm --json \
  --jq '.summary.quants | to_entries | sort_by(-(.value.best_tps // -1))[]
        | {quant: .key, tps: .value.best_tps, ttft_ms: .value.best_ttft_ms,
           memory_mb: .value.best_memory_mb, accuracy: .value.best_accuracy}'

# LLM: which device produced each best, when the user wants the per-device view.
melange report view "$model_key" -R "$repo" --type llm --json \
  --jq '[.records[] | select(.metric == "tps" and .device != null)]
        | group_by(.device.marketing_name)[] | max_by(.value)
        | {device: .device.marketing_name, tps: .value, quant: .quant_type,
           ap_type}'

# General: every device, best latency per accelerator plus signal quality.
melange report view "$model_key" -R "$repo" --type general --json \
  --jq '[.records[] | select(.device != null)]
        | group_by(.device.marketing_name)[]
        | {device: .[0].device.marketing_name, soc: .[0].device.soc,
           cpu_ms: ([.[] | select(.metric == "latency_ms" and .ap_type == "cpu") | .value] | min),
           gpu_ms: ([.[] | select(.metric == "latency_ms" and .ap_type == "gpu") | .value] | min),
           npu_ms: ([.[] | select(.metric == "latency_ms" and .ap_type == "npu") | .value] | min),
           snr_db: ([.[] | select(.metric == "snr_db") | .value] | max)}'
```

A real report is large — TinyLlama returns 5040 records over 50 devices. When a
device table is too long to print in full, sort it, show the top rows, and state
the count you left out and how to get them. Never let a truncated view read as
the complete set.

### Headline metrics

```
╭ Throughput ──╮╭ First token ─╮╭ Memory ──────╮╭ Model size ──╮
│ up to        ││ as low as    ││ smallest as  ││ smaller by   │
│ 286.30 TPS   ││ 13.13 ms     ││ 47.11 MB     ││ x4.63 vs ORG │
╰──────────────╯╰──────────────╯╰──────────────╯╰──────────────╯
```

LLM reports (`--type llm`), each from `report view ... --json`:

| Card | Value |
|------|-------|
| Throughput | `[.summary.quants[].best_tps] \| map(select(. != null)) \| max` |
| First token | `[.summary.quants[].best_ttft_ms] \| map(select(. != null)) \| min` |
| Memory | `[.summary.quants[].best_memory_mb] \| map(select(. != null)) \| min` |
| Model size | ORG ÷ smallest quantized, from `model_size_bytes` records; when the report carries none, from `model targets` `download_size` |

General reports (`--type general`) use the same panel with different cards:

```
╭ Latency ─────╮╭ NPU speedup ─╮╭ Memory ──────╮╭ Signal ──────╮
│ as low as    ││ up to        ││ smallest as  ││ up to        │
│ 23.51 ms     ││ x12.52       ││ 0.97 MB      ││ 85.19 dB     │
╰──────────────╯╰──────────────╯╰──────────────╯╰──────────────╯
```

| Card | Value |
|------|-------|
| Latency | `.summary.latency_ms.all.min` |
| NPU speedup | per device `min(cpu latency_ms) / min(npu latency_ms)`, then the max across devices |
| Memory | `[.records[] \| select(.metric=="memory_inference_peak_mb") \| .value] \| min` |
| Signal | `.summary.snr_db.all.max` |

```sh
# Model size, LLM — most reports carry no model_size_bytes records
melange report view "$model_key" -R "$repo" --type llm --json \
  --jq '[.records[] | select(.metric == "model_size_bytes")] as $s
        | ([$s[] | select(.quant_type == "org") | .value] | max) as $org
        | ([$s[] | select(.quant_type != "org") | .value] | min) as $small
        | if $org and $small and $small > 0 then $org / $small else null end'

# Fall back to the converted artifacts, which carry a size for every quant
melange model targets "$model_key" -R "$repo" --json \
  --jq '[.results[] | {quant: .quant_type, mb: (.download_size / 1048576)}] as $t
        | ([$t[] | select(.quant == "org") | .mb] | max) as $org
        | ([$t[] | select(.quant != "org") | .mb] | min) as $small
        | if $org and $small and $small > 0
          then {smallest_mb: $small, ratio: ($org / $small)} else null end'

# NPU speedup, general models
melange report view "$model_key" -R "$repo" --type general --json \
  --jq '[[.records[] | select(.metric == "latency_ms" and .device != null)]
         | group_by(.device.marketing_name)[]
         | {device: .[0].device.marketing_name,
            cpu: ([.[] | select(.ap_type == "cpu") | .value] | min),
            npu: ([.[] | select(.ap_type == "npu") | .value] | min)}
         | select(.cpu != null and .npu != null and .npu > 0)
         | {device, gain: (.cpu / .npu)}] | max_by(.gain)'
```

Rules for the panel:

- **Drop a card rather than estimate one.** Real reports are sparse:
  `best_memory_mb` is often null, and accuracy scores and `model_size_bytes`
  records are frequently absent. Follow the fallback chain first — for model
  size, the report records, then `model targets`, then accuracy — and only
  omit the card when every source is empty.
- Name what a ratio compares. `x4.63 vs ORG` means the smallest quantization
  against the original weights; the NPU speedup is CPU-versus-NPU latency on
  one device. Never divide a value from one accelerator by a value from
  another — the result is not a speedup.
- `quant_type` is always lowercase in public output (`org`, `f16`, `bf16`,
  `q8_0`, `q6_k`, `q4_k_m`, `q3_k_m`, `q2_k`, `iq2_s`).
- `accuracy_score` and `model_size_bytes` records have `device: null` and
  `ap_type: null`. Filter them out before grouping by device.
- The dashboard shows a "deployability" percentage. It is a threshold widget in
  the web UI, not an API field, and no melange command returns it. Say it is
  not available rather than producing a number for it.
- Every card is a model-wide best across all devices. Never caption the panel
  with one device's name; if you want to say where a figure came from, look the
  device up in `records[]` and attribute that figure alone.
- Device counts are entitlement-limited, not model properties — the same model
  reports far fewer devices on a smaller plan. Label any count as the devices
  visible to the account.

## Browse the public library and your usage

```sh
melange library list --task vision --provider Zetic --jq '.results[].full_name'
melange library view zetic/whisper-tiny --json     # includes the full readme
melange library providers --jq '.results[] | select(.model_count>=10) | .name'

melange usage --jq .prompts                        # this period's counters
melange usage quotas --jq .model_uploads.remaining # headroom now; null = unlimited
melange plan --jq .plan                            # free|lite|pro|pro_plus|enterprise
```

`library list` filters map to query params (`--task` repeats; a model
matching ANY task is included). Its `--search` is case- and
separator-insensitive across both `name` and `full_name`; hyphens,
underscores, slashes, and spaces are ignored. `usage quotas` renders each
quota as `used/limit (pct%)`, or `unlimited` when the limit is null; its
`--json`/`--jq` output also carries `remaining` per counter — the amount the
server will actually allow now (spike headroom included, floored at 0; null =
unlimited). Prefer `remaining` over `limit - used` for preflight.

`melange plan` reports the account's effective plan (`plan`, plus `is_trial`
and `trial_ends_at`). It reflects what the server enforces: an account that
bypasses quota limits reports `pro_plus` and unlimited quotas, exactly as the
dashboard shows.

### Entitlement disclosure (required)

Before uploading a model or describing benchmark coverage as complete, inspect
`melange usage quotas --json`. If `model_uploads.remaining == 0`, do not attempt
or retry an upload: tell the user they have no upload quota left under their
current plan, not that the CLI is broken. Report records contain only devices
visible to the authenticated account; never call them the full benchmark unless
completeness is established.

Plan identity is knowable — read it from `melange plan --jq .plan`. When
`model_uploads.remaining == 0` and the plan is **Free** (or **Lite**), say that
the plan is why model upload is locked; on other plans say the monthly upload
quota is exhausted. Do not attribute a zero to the wrong cause: check `melange
plan` rather than inferring Free from a zero limit, a filtered report, or an
HTTP error.

Library `ACCOUNT/NAME` values identify public repositories, not converted model
keys. Inspect their existing models and reports directly; this is a read-only
path and needs no import or upload:

```sh
repo=$(melange library list --search QUERY --jq '.results[0].full_name')
key=$(melange model list -R "$repo" --jq '.results | (map(select(.is_default and .state=="ready")) + map(select(.state=="ready")))[0].key // empty')
[ -n "$key" ] || { echo "No ready model is available in $repo" >&2; exit 1; }
melange report view "$key" -R "$repo" --json
```

Never import a library model solely to read its public benchmarks.

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
snippets; JSON is the structured public response. The model repository coordinate, version,
family, selected mode, and SDK language are already resolved. General-model
tensor creation remains an explicit TODO because shapes and preprocessing are
model-specific—inspect tensor I/O metadata rather than inventing inputs.

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
- Never parse TTY tables — use `--json` or `--jq`. Plain `--json` is
  the API response bytes with the terminator normalized to exactly one trailing
  newline, except for waited upload/import composites and redacted model
  download authorization URLs.
- Exit 130 means the upload session is preserved; resume with the
  printed `--resume` line instead of restarting the upload.
- Exit 2 = your invocation is wrong (don't retry); exit 4 = credentials
  (don't retry until fixed).
- `--jq` output re-marshals (sorted keys); use plain `--json` when you
  need the original server payload bytes.
- `MELANGE_DEBUG=1` logs request/response lines to stderr (tokens and
  headers are never logged).
- `MELANGE_API_TIMEOUT` bounds ordinary API requests (default `30s`);
  upload inactivity and conversion `--wait` have separate timeout flags.
- Successful `melange api` output is committed only after the complete body
  is read. Increase `MELANGE_API_TIMEOUT` for legitimately slow or large
  responses; a failed read leaves stdout empty instead of emitting partial data.
- Deployment guides accept repository model keys only. Never pass a Hugging
  Face ID to `melange deploy guide`, and never invent SDK fields or callbacks.
- Never run `--wait` in the foreground while a person is waiting; import or
  upload without it, report the phase, and monitor in the background.
- Never present a metric the response did not carry. Drop the card, say the
  value is unavailable, and never compare values measured on different
  accelerators as if one were a speedup.
- Report the whole model, not one device: summary values are bests across all
  devices, so labelling them with a device name misattributes the measurement.
  Print the report in your reply rather than saving it to a file.
