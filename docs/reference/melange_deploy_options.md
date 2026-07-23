## melange deploy options

List supported deployment languages and inference modes

### Synopsis

List the exact deployment selectors supported by public-v1.
React Native is intentionally excluded. Use --json for the structured contract.

```
melange deploy options [flags]
```

### Options

```
  -h, --help              help for options
      --jq expression     Filter JSON output using a jq expression (implies --json)
      --json              Output the full result as JSON
      --template string   Format JSON output using a Go template (implies --json)
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange deploy](melange_deploy.md)	 - Get SDK deployment code for a model
