## melange library

Browse the public model library

### Synopsis

Browse the public Melange model library: list and filter models,
inspect a single model (with its readme), and list the providers.

Data is written to stdout; progress and messages go to stderr. All
subcommands support --json, --jq, and --template for structured output.

### Examples

```
  # List vision models from a provider
  melange library list --task vision --provider Zetic

  # Inspect a library model
  melange library view zetic/whisper-tiny

  # List the providers
  melange library providers
```

### Options

```
  -h, --help   help for library
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
* [melange library list](melange_library_list.md)	 - List public library models
* [melange library providers](melange_library_providers.md)	 - List library providers
* [melange library view](melange_library_view.md)	 - View a library model

