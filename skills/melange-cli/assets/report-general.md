# General model report — template

For every non-LLM model. Fill this in and print the result in your reply. Keep the
section order. Run the commands; do not retype numbers from memory or from a TTY
table.

Set these once:

```sh
repo=ACCOUNT/REPO
model_key="$(melange model list -R "$repo" --jq '.results[] | select(.is_default) | .key')"
```

## Conventions

- Round for display only: latency and SNR → **2 decimals**; memory → **1 decimal**.
- A value the response did not carry prints `-` in a table and `N/A` in a card.
  Keep the row, keep the column, keep the card.
- `.summary.<metric>.all` holds the best across **all devices**. It belongs to no
  single device — never put a device name on it. Per-device figures come from
  `records[]`.
- Print every accelerator × precision column present in the data. If a device table
  is too long, sort it, show the top rows, and say how many you left out.
- `--jq` takes an expression and nothing else; it has no `--arg`.
- `--mode` changes only the aligned TTY table. `--json` and non-TTY output are
  identical across `auto`, `speed`, and `accuracy`, so derive the mode picks
  yourself (section 6) rather than calling the CLI three times.

---

## 1. Identity

```
{Model name} — {ACCOUNT/REPO}
Key {model_key} · v{version} · {state}
Released {Mon D, YYYY} · Model size {smallest} – {largest} MB
```

```sh
melange repo view "$repo" --json --jq '{name: .full_name, description}'
melange model list -R "$repo" --json \
  --jq '.results[] | select(.is_default) | {key, version, state, created_at}'
melange model targets "$model_key" -R "$repo" --json \
  --jq '[.results[] | .download_size / 1048576] | {min: (min*100|round/100), max: (max*100|round/100)}'
```

## 2. Description

One short paragraph: what the model does, its family, where it came from. Use
`.description` from `repo view`, or the readme from `melange library view "$repo"
--json` for a public model.

## 3. Summary

```
╭ Latency ─────╮╭ NPU gain ────╮╭ Memory ──────╮
│ as low as    ││ up to        ││ smallest as  │
│ {N} ms       ││ x{N} vs CPU  ││ {N} MB       │
╰──────────────╯╰──────────────╯╰──────────────╯
```

```sh
melange report view "$model_key" -R "$repo" --type general --json --jq '{
  latency_ms: .summary.latency_ms.all.min,
  memory_mb:  ([.records[] | select(.metric == "memory_inference_peak_mb") | .value] | min)
}'

# NPU gain: CPU vs NPU on the same device, best device wins.
melange report view "$model_key" -R "$repo" --type general --json \
  --jq '[[.records[] | select(.metric == "latency_ms" and .device != null)]
         | group_by(.device.marketing_name)[]
         | {device: .[0].device.marketing_name,
            cpu: ([.[] | select(.ap_type == "cpu") | .value] | min),
            npu: ([.[] | select(.ap_type == "npu") | .value] | min)}
         | select(.cpu != null and .npu != null and .npu > 0)
         | {device, gain: (.cpu / .npu)}] | max_by(.gain)'
```

Name what the ratio compares — CPU versus NPU latency **on one device**. Never
divide a value measured on one accelerator by a value from another.

## 4. Overview

Three tables, each with an All / FP32 / FP16 / INT8 breakdown.

**Latency (ms)**

| Precision | Min | Max | Median | Avg |
|-----------|----:|----:|-------:|----:|

**Output quality (dB)**

| Precision | Min | Max |
|-----------|----:|----:|

**Memory (MB)**

| Precision | Load min | Load max | Inference min | Inference max |
|-----------|---------:|---------:|--------------:|--------------:|

```sh
melange report view "$model_key" -R "$repo" --type general --json --jq '.summary'
```

A precision bucket that is `null` still gets its row, filled with `-`.

## 5. Full device performance benchmark

One row per device; one column per accelerator × precision combination present.

| # | Device | NPU FP32 | NPU FP16 | NPU INT8 | GPU FP32 | GPU FP16 | GPU INT8 | CPU FP32 | CPU FP16 | CPU INT8 |
|---|--------|---------:|---------:|---------:|---------:|---------:|---------:|---------:|---------:|---------:|

Drop only the columns absent from the whole response; keep every column that any
device populated, blanking missing cells with `-`.

```sh
melange report view "$model_key" -R "$repo" --type general --json \
  --jq '["npu_fp32","npu_fp16","npu_int8",
         "gpu_fp32","gpu_fp16","gpu_int8",
         "cpu_fp32","cpu_fp16","cpu_int8"] as $ALL
        | [.records[] | select(.metric == "latency_ms" and .device != null)]
        | ([.[] | .ap_type + "_" + .precision] | unique) as $present
        | ($ALL | map(select(. as $c | $present | index($c)))) as $C
        | (group_by(.device.marketing_name)
           | map({ device: .[0].device.marketing_name,
                   cells: (group_by(.ap_type + "_" + .precision)
                           | map({ key: (.[0].ap_type + "_" + .[0].precision),
                                   value: ([.[].value] | min | .*100|round/100) })
                           | from_entries) })
           | map(.best = ([.cells[]] | min)) | sort_by(.best)) as $rows
        | ([""] + $C),
          ($rows[] | . as $r | [$r.device] + ($C | map($r.cells[.] // "-" | tostring)))'
```

For output quality or memory, replace `"latency_ms"` with `"snr_db"` or
`"memory_inference_peak_mb"`. SNR is better when higher — use `max` and
`sort_by(-.best)` for it.

## 6. Inference mode performance

Latency each mode would select, per device.

| # | Device | Auto | Speed | Accuracy |
|---|--------|-----:|------:|---------:|

The picks: **speed** = lowest latency; **accuracy** = highest SNR, ties to lower
latency; **auto** = fastest run above 20 dB, falling back to speed.

```sh
melange report view "$model_key" -R "$repo" --type general --json \
  --jq '[.records[] | select(.device != null)]
        | group_by(.device.marketing_name)
        | map({ device: .[0].device.marketing_name,
                runs: (group_by([.ap_type, .variant, .precision, .run])
                       | map({ lat: ([.[] | select(.metric == "latency_ms") | .value] | first),
                               snr: ([.[] | select(.metric == "snr_db") | .value] | first) })
                       | map(select(.lat != null))) })
        | map({ device,
                speed:    ([.runs[].lat] | min),
                accuracy: (.runs | map(select(.snr != null)) | sort_by(-.snr, .lat) | first | .lat),
                auto:     ((.runs | map(select(.snr != null and .snr > 20)) | map(.lat) | min)
                           // ([.runs[].lat] | min)) })
        | sort_by(.auto)[]'
```

## 7. Coverage

State the device count and that it reflects what this account's plan can see:

```sh
melange plan --jq .plan
melange report view "$model_key" -R "$repo" --type general --json \
  --jq '[.records[] | select(.device != null) | .device.marketing_name] | unique | length'
```

---

## Reporting one device instead

Only when the user names a device. Say plainly that it is a device slice, and take
it from `records[]` — never by relabeling `.summary`.

```sh
melange report view "$model_key" -R "$repo" --type general --json \
  --jq '[.records[] | select(.metric == "latency_ms"
         and .device.marketing_name == "Google Pixel 10")]
        | group_by(.ap_type + "_" + .precision)[]
        | {bucket: (.[0].ap_type + "_" + .[0].precision),
           best_ms: (map(.value) | min | .*100|round/100)}'
```
