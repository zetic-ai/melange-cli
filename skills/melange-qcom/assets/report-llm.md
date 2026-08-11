# Qualcomm LLM report

Fill this template from `melange-qcom` output and print it in the reply.

Set:

```sh
repo=ACCOUNT/REPO
model_key="$(melange-qcom model list -R "$repo" --jq '.results[] | select(.is_default) | .key')"
report="$(melange-qcom report view "$model_key" -R "$repo" --type llm --json)"
```

## Identity

```text
{Model name} — {ACCOUNT/REPO}
Key {model_key} · v{version} · {state}
```

## Qualcomm coverage

State `.qualcomm_filter.matched_devices`, hidden non-Qualcomm record count, and
hidden unclassified record count. Device-less accuracy records are model-level
and remain present; do not count them as Qualcomm device measurements.

## Summary

```text
╭ Throughput ──╮╭ First token ─╮╭ Memory ──────╮
│ up to        ││ as low as    ││ smallest as  │
│ {N} TPS      ││ {N} ms       ││ {N} MB       │
╰──────────────╯╰──────────────╯╰──────────────╯
```

Use recomputed `.summary.quants`. Render missing values as `N/A`.

## Per-quantization summary

| Quant | Throughput | First token | Peak memory | Accuracy |
|-------|-----------:|------------:|------------:|---------:|

Sort by retained Qualcomm throughput. Accuracy may come from device-less
model-level records; label it accordingly.

## Qualcomm device benchmark

| Device | SoC | Quant | Accelerator | TPS | TTFT | Peak memory |
|--------|-----|-------|-------------|----:|-----:|------------:|

Build rows only from retained device records. Round TPS/TTFT to two decimals
and memory to one decimal. Never assign model-level accuracy to one device.

## Deployment

Device-unscoped LLM targets are expected and retained. Choose by quantization
and size, then provide the exact Android or Flutter deployment guide.
