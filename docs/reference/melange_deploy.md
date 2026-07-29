## melange deploy

Get SDK deployment code for a model

### Synopsis

Inspect the supported SDK stacks and render deterministic deployment
code for a specific model version. Guides use the public credential placeholder
YOUR_PERSONAL_KEY; melange never writes the active PAT into code or output.

```
melange deploy <command> [flags]
```

### Options

```
  -h, --help   help for deploy
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
* [melange deploy guide](melange_deploy_guide.md)	 - Print exact SDK deployment code for a model
* [melange deploy options](melange_deploy_options.md)	 - List supported deployment languages and inference modes
