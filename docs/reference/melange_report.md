## melange report

Read model benchmark reports

### Synopsis

Read the benchmark reports of a converted model: general (per-device
latency/SNR/memory), LLM (per-quant tokens/sec and accuracy), and
package (ZTC per-mode metrics).

On a terminal, `report view` renders the dashboard-grade table: the
mode columns are re-derived from the raw records by the pinned selection
rule (speed = lowest latency; accuracy = highest SNR, ties to lower
latency; auto = fastest run whose SNR exceeds 20 dB, else the speed run).
When stdout is not a terminal it prints one raw record per line as
tab-separated values — scripts get the measurements, not the derived
table. With --json the API response fields and order are preserved, followed
by exactly one trailing newline.

Data is written to stdout; progress and messages go to stderr.

```
melange report <command> [flags]
```

### Examples

```
  # Prefer the public repository's default model; fall back to a ready model
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results | (map(select(.is_default and .state=="ready")) + map(select(.state=="ready")))[0].key // empty')
  [ -n "$model_key" ] || { echo "No ready model is available" >&2; exit 1; }

  # The dashboard table for that model's general report
  melange report view "$model_key" -R zetic/whisper-tiny

  # Fill the table with the accuracy-mode pick instead of auto
  melange report view "$model_key" -R zetic/whisper-tiny --mode accuracy

  # Agent pattern: best NPU latency per device, from the raw records
  melange report view "$model_key" -R zetic/whisper-tiny --json \
    --jq '[.records[] | select(.ap_type=="npu" and .metric=="latency_ms")]
          | group_by(.device.marketing_name)[]
          | {device: .[0].device.marketing_name, best: (map(.value) | min)}'
```

### Options

```
  -h, --help   help for report
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
* [melange report view](melange_report_view.md)	 - View a model's benchmark report
