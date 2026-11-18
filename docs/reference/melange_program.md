## melange program

Read partner program benchmark sheets

### Synopsis

Read the benchmark sheets of a partner program: results per SoC for
the program's models, measured and published by Zetic. Only program members
and Zetic staff can read a program.

List the programs you belong to with:
  melange api /v1/me --jq '.access_programs[].name'

Data is written to stdout; progress and messages go to stderr. All
subcommands support --json, --jq, and --template for structured output.

```
melange program <command> [flags]
```

### Examples

```
  # The programs you belong to
  melange api /v1/me --jq '.access_programs[].name'

  # Every sheet of one of them
  melange program benchmarks PROGRAM

  # One model on one SoC, as JSON
  melange program benchmarks PROGRAM --model MODEL_SLUG --soc SOC --json
```

### Options

```
  -h, --help   help for program
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
* [melange program benchmarks](melange_program_benchmarks.md)	 - View a program's benchmark sheets
