## melange program benchmarks

View a program's benchmark sheets

### Synopsis

Show the benchmark sheets of a partner program. PROGRAM is the program
name, as listed in access_programs by melange api /v1/me.

Results are per SoC. Each model's sheet on a SoC lists Zetic's builds by
inference mode, like a repository model page: a build's "modes" names the
modes it serves (speed, accuracy), at most one build per mode (per variant
when the model has variants), and Auto follows Speed. A sheet can also carry
builds that serve no mode, such as a float reference for accuracy. Which
metrics a model has depends on its task (llm, asr, tts,
image_classification); the response's "metrics" object describes each one
(label, unit, which direction is better, and its category: speed, memory,
energy, size or accuracy).

--model keeps one model (by slug) and --soc one SoC; with --soc, models
without a result on that SoC are left out. A model or SoC the program does
not list exits 1 and names the ones it does.

On a terminal this prints, per model and SoC, rows Auto / Speed / Accuracy
(per variant) with the build serving each and the task's summary metrics,
grouped by category (speed, memory, energy, accuracy); a mode no build
serves is left out. Builds that serve no mode follow with mode "-". Keyed
metrics print every key (prompt length or test set). The measurement
method, notes and extra tables are in --json.

When stdout is not a terminal it prints one metric value per line as
tab-separated values with no header, stable for scripts: soc, model, task,
variant, build, runtime, status, modes, metric, key, value, unit. modes is
the build's modes comma-joined
(empty when it serves none); key is empty for unkeyed metrics; a build with
no metrics (status unsupported or not_measured) prints one line with empty
metric, key, value and unit. Values are the API's numbers verbatim.

With --json the API response is emitted with its fields and order
preserved (narrowed by --model/--soc), followed by exactly one trailing
newline.

A program you are not a member of answers 404, the same as a program that
does not exist.

Exit codes: 0 success, 1 API error (including not found) or unknown
--model/--soc, 2 usage error, 4 not authenticated.

```
melange program benchmarks PROGRAM [flags]
```

### Examples

```
  # Every sheet of a program
  melange program benchmarks PROGRAM

  # One model on one SoC
  melange program benchmarks PROGRAM --model MODEL_SLUG --soc SOC

  # The models and SoCs a program lists
  melange program benchmarks PROGRAM --json --jq '{models: [.models[].slug], socs: [.targets[].soc]}'

  # Agent pattern: decode speed of every LLM build, per SoC
  melange program benchmarks PROGRAM --json \
    --jq '.models[] | select(.task=="llm") | .slug as $m | .results[] | .soc as $s
          | .builds[] | {model: $m, soc: $s, build: .key, decode_tps: .speed.decode_tps}'
```

### Options

```
  -h, --help              help for benchmarks
      --jq expression     Filter JSON output using a jq expression (implies --json)
      --json              Output the full result as JSON
      --model slug        Keep one model, by slug
      --soc SoC           Keep one SoC from the program's catalog
      --template string   Format JSON output using a Go template (implies --json)
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange program](melange_program.md)	 - Read partner program benchmark sheets
