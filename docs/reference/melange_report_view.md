## melange report view

View a model's benchmark report

### Synopsis

Render a model's benchmark report.

--type selects the report: general, llm, or package. Without it the CLI
probes in order — general, then llm, then package — and shows the first
one that exists; when the model has none it exits 1 "no report available".

On a terminal:
  * general — one row per device; a column per (ap_type × precision)
    present, each cell the mode pick's latency in ms (1 decimal). --mode
    (auto, speed, or accuracy) chooses which pick fills the cells; auto is
    the default. Below the table a per-precision summary block lists
    latency min/median/max, the SNR range, and the memory range.
  * llm — rows are devices, columns are quant types, cells are tokens/sec
    (1 decimal); an accuracy section follows, per dataset.
  * package — a mode × metric table.
Missing cells render "-". Devices are sorted alphabetically.

When stdout is not a terminal it prints one raw record per line as
tab-separated values (the flat measurement fields) — scripts get the
records, not the derived table. With --json the API response is emitted
byte-for-byte.

Exit codes: 0 success, 1 API error (including no report), 2 usage error,
4 not authenticated.

```
melange report view MODEL_KEY [flags]
```

### Examples

```
  # The dashboard table (auto-derived mode picks)
  melange report view m_ab12cd -R zetic/whisper-tiny

  # Force the LLM report and fill cells with the speed pick
  melange report view m_ab12cd -R zetic/whisper-tiny --type llm --mode speed

  # Agent pattern: best NPU latency per device, from the raw records
  melange report view m_ab12cd -R zetic/whisper-tiny --json \
    --jq '[.records[] | select(.ap_type=="npu" and .metric=="latency_ms")]
          | group_by(.device.marketing_name)[]
          | {device: .[0].device.marketing_name, best: (map(.value) | min)}'
```

### Options

```
  -h, --help                help for view
      --jq expression       Filter JSON output using a jq expression (implies --json)
      --json                Output the full result as JSON
      --mode string         Mode pick for general cells: auto, speed, or accuracy (default "auto")
  -R, --repo ACCOUNT/REPO   Repository as ACCOUNT/REPO (required)
      --template string     Format JSON output using a Go template (implies --json)
      --type type           Report type: general, llm, or package (default: probe)
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange report](melange_report.md)	 - Read model benchmark reports

