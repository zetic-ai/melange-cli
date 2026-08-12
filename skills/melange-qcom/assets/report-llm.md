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
hidden unclassified record count. Device-less records (model-level accuracy,
model size) remain present; do not count them as Qualcomm device measurements.

## Summary

```text
╭ Throughput ──╮╭ First token ─╮╭ Memory ──────╮
│ up to        ││ as low as    ││ smallest as  │
│ {N} TPS      ││ {N} ms       ││ {N} MB       │
╰──────────────╯╰──────────────╯╰──────────────╯
```

Use recomputed `.summary.quants`. Render missing values as `N/A`.

## Per-quantization summary

| Quant | Throughput | First token | Peak memory | Accuracy | Perplexity |
|-------|-----------:|------------:|------------:|---------:|-----------:|

Sort by retained Qualcomm throughput. Accuracy may come from device-less
model-level records; label it accordingly.

## Perplexity

Lower is better. `best_perplexity` per quant is recomputed from retained
records:

```sh
printf '%s\n' "$report" | jq '.summary.quants | to_entries[]
  | {quant: .key, best_ppl: .value.best_perplexity}'
```

Perplexity below `.summary.ppl_min_scored_tokens` scored tokens is never
published. A report without perplexity keeps the column: `-` in tables, `N/A`
in cards (`.summary.has_perplexity_attempt` says whether it was attempted).

## Qualcomm device benchmark

| Device | SoC | Quant | Accelerator | TPS | TTFT | Peak memory |
|--------|-----|-------|-------------|----:|-----:|------------:|

Build rows only from retained device records. Round TPS/TTFT to two decimals
and memory to one decimal. Never assign model-level accuracy to one device.

## Deployment

Device-unscoped LLM targets are expected and retained. Choose by quantization
and size, then provide the exact Android or Flutter deployment guide.
