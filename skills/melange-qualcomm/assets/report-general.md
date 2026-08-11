# Qualcomm general-model report

Fill this template from `melange-qualcomm` output and print it in the reply.

Set:

```sh
repo=ACCOUNT/REPO
model_key="$(melange-qualcomm model list -R "$repo" --jq '.results[] | select(.is_default) | .key')"
report="$(melange-qualcomm report view "$model_key" -R "$repo" --type general --json)"
```

## Identity

```text
{Model name} — {ACCOUNT/REPO}
Key {model_key} · v{version} · {state}
```

Use `repo view`, `model list`, and `model targets`; do not infer identity from a
display name.

## Qualcomm coverage

State `.qualcomm_filter.matched_devices`, hidden non-Qualcomm record count, and
hidden unclassified record count. Call this the reviewed Qualcomm fleet, not
every Qualcomm device. Stop if the report command exits 1.

## Summary

```text
╭ Latency ─────╮╭ NPU gain ────╮╭ Memory ──────╮
│ as low as    ││ up to        ││ smallest as  │
│ {N} ms       ││ x{N} vs CPU  ││ {N} MB       │
╰──────────────╯╰──────────────╯╰──────────────╯
```

Use `.summary.latency_ms.all.min` and retained `records`. Compute NPU gain only
from CPU and NPU latency on the same retained device.

## Precision overview

| Precision | Latency min/median/max | SNR min/max | Memory load/inf range |
|-----------|-----------------------:|------------:|----------------------:|

Read `.summary`. Keep null precision rows and render missing values as `-`.

## Qualcomm device benchmark

| Device | SoC | CPU | GPU | NPU |
|--------|-----|----:|----:|----:|

Build cells from retained `records[]`, grouping by exact device and SoC. State
the metric, precision, inference mode, and units. Round latency/SNR to two
decimals and memory to one decimal.

## Deployment

Name the selected Qualcomm or device-unscoped target, then provide the exact
Android or Flutter guide from `melange-qualcomm deploy guide`.
