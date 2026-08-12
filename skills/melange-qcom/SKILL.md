---
name: melange-qcom
description: "Use when operating the ZETIC Melange platform for Qualcomm-focused on-device AI work with the `melange-qcom` CLI or MCP server: model upload/import and conversion monitoring, Qualcomm benchmark reports, converted target selection, and Android or Flutter deployment guides. Trigger for Qualcomm device evaluation, Snapdragon/QTI benchmark questions, or Qualcomm deployment workflows. Do not use the general `melange` binary for these requests."
---

# Melange Qualcomm

Use `melange-qcom` for the complete workflow. It shares Melange accounts,
credentials, repositories, and local state with `melange`, but filters report
and target presentation to the reviewed Qualcomm fleet.

## Choose CLI or MCP

Prefer the connected Qualcomm MCP server when its tools are available. Start it
with `melange-qcom mcp`; use the CLI for auth, local downloads, advanced
uploads, or raw API access.

The `api` command is an intentionally unfiltered escape hatch. Never describe
its report, target, device, or deployment data as Qualcomm-filtered. Apply and
disclose filtering yourself when the dedicated command or MCP tool cannot do
the job.

## Authenticate and read structured output

```sh
melange-qcom auth login
melange-qcom auth status --json
```

Use `MELANGE_API_KEY` or `MELANGE_API_KEY_FILE` for headless environments. Pass
`--no-input` in scripts. Branch on exit codes: 0 success, 1 API/operation error,
2 invocation error, 4 authentication error, and 130 interruption.

Use `--json` or `--jq` for agent work. Standard commands keep Melange's JSON
contract. Qualcomm report and target commands return a re-marshaled filtered
envelope with `qualcomm_filter` metadata; do not claim byte-exact API passthrough
for those commands.

Keep identifiers distinct:

- `ACCOUNT/REPO` identifies a repository.
- `MODEL_KEY` identifies one converted model version.
- `TARGET_ID` identifies one downloadable artifact.

Resolve them in that order and never parse opaque model or target identifiers.

## Manage and convert models

Check entitlement immediately before uploading:

```sh
melange-qcom plan --jq .plan
melange-qcom usage quotas --json
```

If `.model_uploads.remaining == 0`, stop. Attribute the restriction to the Free
or Lite plan only after reading `plan`; otherwise call it an exhausted monthly
quota.

Create a repository, validate the local manifest, then start conversion without
blocking the user:

```sh
repo="$(melange-qcom repo create demo --private --jq .full_name)"
melange-qcom model upload -R "$repo" model.onnx --input sample.npy --dry-run --json
created="$(melange-qcom model upload -R "$repo" model.onnx --input sample.npy --json)"
model_key="$(printf '%s\n' "$created" | jq -er .model.key)"
melange-qcom model status "$model_key" -R "$repo" --json
```

For a Hugging Face model, the path depends on its type, and model type is fixed
at `repo create`. Classify first with
`curl -fsSL "https://huggingface.co/api/models/OWNER/NAME" | jq -r .pipeline_tag`:
`text-generation` imports the repo id, everything else ships as artifacts.

```sh
llm_repo="$(melange-qcom repo create demo-llm --private --model-type llm --jq .full_name)"
melange-qcom model import ORG/MODEL -R "$llm_repo" --json
```

For a general model, tell the user what the artifact path involves before
starting it: a local export to `.pt2` plus `.npy` sample inputs, which downloads
the weights, needs PyTorch >= 2.9, and fixes the input shape the deployed model
will accept. Then export and upload as above. See
<https://docs.zetic.ai/model-preparation/pytorch-export>.

Do not use `--wait` while a person is waiting. Report the current public state
(`converting`, `optimizing`, `ready`, or `failed`) and monitor in the background.
At `optimizing`, say artifacts are already downloadable while benchmarks finish.
Never invent a percentage: public `progress` is null.

Render this phase panel after upload, import, and status checks:

```text
╭─ Conversion Pipeline ───────────────────────────────╮
│   ✔ CONVERTING   →   ◐ OPTIMIZING   →   ○ READY     │
│     complete           in progress       pending    │
╰─────────────────────────────────────────────────────╯
```

Use `✔` for completed, `◐` for current, `○` for pending, and `✖` for failure.
On failure, report `.stage` and `.failure_code` verbatim without guessing.

## Report Qualcomm benchmarks

Resolve a ready model, then request its report:

```sh
model_key="$(melange-qcom model list -R "$repo" \
  --jq '.results | (map(select(.is_default and .state=="ready")) + map(select(.state=="ready")))[0].key // empty')"
melange-qcom report view "$model_key" -R "$repo" --json
```

The CLI matches exact reviewed `(marketing_name, soc)` pairs. It hides known
non-Qualcomm and unclassified device records, recomputes summaries, and returns:

```json
{
  "qualcomm_filter": {
    "strategy": "fixed-fleet-v1",
    "matched_devices": 1,
    "hidden_non_qualcomm_records": 10,
    "hidden_unclassified_records": 0
  }
}
```

Treat the matched set as the reviewed Qualcomm fleet, not every Qualcomm device
in existence. If the command exits 1 with no Qualcomm measurements, do not fall
back to general `melange report` output.

Read and fill the matching template before answering:

- General models: `assets/report-general.md`
- LLMs: `assets/report-llm.md`

Print the report in the reply. Preserve missing values as `-` in tables and
`N/A` in cards. Never compare metrics from different accelerators as a speedup.

## Select Qualcomm targets

```sh
melange-qcom model targets "$model_key" -R "$repo" --json
```

The command retains targets whose `compatibility.soc_manufacturer` is Qualcomm,
retains compatibility-null device-unscoped artifacts used by LLM/universal
targets, and hides explicitly other vendors. Inspect `qualcomm_filter` before
claiming target coverage. Choose artifacts from precision, quantization,
compatibility, accelerator classes, and size—never from an inferred engine.

Download only after the user authorizes the billable operation:

```sh
melange-qcom model download "$model_key" -R "$repo" \
  --target TARGET_ID --output ./models --yes
```

## Generate deployment code

The Qualcomm edition supports `android-kotlin`, `android-java`, and `flutter`.
It rejects `ios-swift`. Kotlin and `auto` are defaults.

```sh
melange-qcom deploy options --json
melange-qcom deploy guide "$model_key" -R "$repo" \
  --language android-kotlin --mode auto
melange-qcom deploy guide "$model_key" -R "$repo" \
  --language flutter --mode speed --json
```

Use the guide's exact SDK fields and callbacks. Keep `YOUR_PERSONAL_KEY` as the
placeholder; never interpolate, print, or persist the active credential.
General-model tensor construction may remain a TODO when shapes and
preprocessing are model-specific.

## Resume interrupted uploads

Prefer the resume command printed when exit 130 occurred. Otherwise locate the
active session and resume by its opaque id:

```sh
session_id="$(melange-qcom model upload --sessions -R "$repo" \
  --jq '.results | map(select(.state=="CREATED" or .state=="UPLOADING")) | first | .id // empty')"
test -n "$session_id"
melange-qcom model upload --resume "$session_id" -R "$repo"
```

Run uploads with `--dry-run` first, never retry exit 2 or 4, and never present a
metric or device classification the filtered response did not carry.
