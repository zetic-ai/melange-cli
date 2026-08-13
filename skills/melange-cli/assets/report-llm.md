# LLM model report — template

Fill this in and print the result in your reply. Keep the section order. Run the
commands; do not retype numbers from memory or from a TTY table.

Set these once:

```sh
repo=ACCOUNT/REPO
model_key="$(melange model list -R "$repo" --jq '.results[] | select(.is_default) | .key')"
```

## Conventions

- Round for display only: TPS, TTFT → **2 decimals**; memory → **1 decimal**;
  model size → **2 decimals**.
- A value the response did not carry prints `-` in a table and `N/A` in a card.
  Keep the row, keep the column, keep the card.
- `.summary.quants` holds the best across **all devices**. It belongs to no single
  device — never put a device name on it. Per-device figures come from `records[]`.
- Print every quantization column, including empty ones. If a device table is too
  long, sort it, show the top rows, and say how many you left out.
- `--jq` takes an expression and nothing else; it has no `--arg`. Substitute values
  into the expression text yourself.
- `--mode` is a general-report flag. Passing it here exits 2.

---

## 1. Identity

```
{Model name} — {ACCOUNT/REPO}
Key {model_key} · v{version} · {state}
Released {Mon D, YYYY} · Model size {smallest} – {largest} MB · {n} quantizations
```

```sh
melange repo view "$repo" --json --jq '{name: .full_name, description}'
melange model list -R "$repo" --json \
  --jq '.results[] | select(.is_default) | {key, version, state, created_at}'
melange model targets "$model_key" -R "$repo" --json \
  --jq '[.results[] | .download_size / 1048576] | {min: (min*100|round/100), max: (max*100|round/100), quants: length}'
```

## 2. Description

One short paragraph: what the model does, its family and parameter count, where it
came from. Use `.description` from `repo view`, or the readme from
`melange library view "$repo" --json` for a public model.

## 3. Summary

```
╭ Throughput ──╮╭ Memory ──────╮
│ up to        ││ smallest as  │
│ {N} TPS      ││ {N} MB       │
╰──────────────╯╰──────────────╯
```

```sh
melange report view "$model_key" -R "$repo" --type llm --json --jq '{
  throughput_tps: ([.summary.quants[].best_tps] | map(select(. != null)) | max),
  memory_mb:      ([.summary.quants[].best_memory_mb] | map(select(. != null)) | min)
}'
```

## 4. Per-quantization summary

| Quant | Throughput (TPS) | First token (ms) | Peak memory (MB) | Accuracy | Perplexity |
|-------|-----------------:|-----------------:|-----------------:|---------:|-----------:|

One row per quantization, best across all devices, sorted by throughput.

```sh
melange report view "$model_key" -R "$repo" --type llm --json \
  --jq '.summary.quants | to_entries | sort_by(-(.value.best_tps // -1))[]
        | {quant: .key, tps: .value.best_tps, ttft_ms: .value.best_ttft_ms,
           memory_mb: .value.best_memory_mb, accuracy: .value.best_accuracy,
           perplexity: .value.best_perplexity}'
```

## 5. Perplexity

Lower is better. `best_perplexity` per quant, plus the reliability threshold:

```sh
melange report view "$model_key" -R "$repo" --type llm --json \
  --jq '.summary.quants | to_entries[] | {quant: .key, best_ppl: .value.best_perplexity}'
melange report view "$model_key" -R "$repo" --type llm --json \
  --jq '{attempted: .summary.has_perplexity_attempt, min_scored_tokens: .summary.ppl_min_scored_tokens}'
```

Values below `ppl_min_scored_tokens` scored tokens are never published, so
every published perplexity is full-budget-comparable. A report without
perplexity keeps the column: `-` in tables, `N/A` in cards
(`has_perplexity_attempt` says whether a measurement was even attempted).
Per-device perplexity comes from `records[]`:

```sh
melange report view "$model_key" -R "$repo" --type llm --json \
  --jq '[.records[] | select(.metric == "perplexity" and .device != null)]
        | group_by(.device.marketing_name)[]
        | {device: .[0].device.marketing_name, best_ppl: (map(.value) | min)}'
```

## 6. Full device performance benchmark

One row per device, one column per quantization.

| # | Device | ORG | F16 | BF16 | Q8_0 | Q6_K | Q4_K_M | Q3_K_M | Q2_K |
|---|--------|----:|----:|-----:|-----:|-----:|-------:|-------:|-----:|

Each cell is the best run on that device at that quantization, suffixed with the
accelerator that produced it: `N` NPU, `G` GPU, `C` CPU.

```sh
melange report view "$model_key" -R "$repo" --type llm --json \
  --jq '["org","f16","bf16","q8_0","q6_k","q4_k_m","q3_k_m","q2_k"] as $Q
        | [.records[] | select(.metric == "tps" and .device != null)]
        | group_by(.device.marketing_name)
        | map({ device: .[0].device.marketing_name,
                cells: (group_by(.quant_type)
                        | map({ key: .[0].quant_type,
                                value: (max_by(.value)
                                        | {v: (.value*100|round/100),
                                           ap: (.ap_type[0:1] | ascii_upcase)}) })
                        | from_entries) })
        | map(.best = ([.cells[].v] | max)) | sort_by(-.best)
        | .[] | . as $r
        | [$r.device] + ($Q | map($r.cells[.] as $c
                                  | if $c then "\($c.v) \($c.ap)" else "-" end))'
```

For first-token latency or memory, replace `"tps"` with `"ttft_ms"` or
`"memory_inference_peak_mb"`, swap `max_by` for `min_by`, sort ascending
(`sort_by(.best)`), and say lower is better.

## 7. Coverage

State the device count and that it reflects what this account's plan can see:

```sh
melange plan --jq .plan
melange report view "$model_key" -R "$repo" --type llm --json \
  --jq '[.records[] | select(.device != null) | .device.marketing_name] | unique | length'
```

---

## Reporting one device instead

Only when the user names a device. Say plainly that it is a device slice, and take
it from `records[]` — never by relabeling `.summary`.

```sh
melange report view "$model_key" -R "$repo" --type llm --json \
  --jq '[.records[] | select(.metric == "tps"
         and .device.marketing_name == "Google Pixel 10")]
        | group_by(.quant_type)[]
        | {quant: .[0].quant_type, best_tps: (map(.value) | max | .*100|round/100)}'
```
