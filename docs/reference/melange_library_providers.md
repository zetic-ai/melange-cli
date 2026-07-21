## melange library providers

List library providers

### Synopsis

List the providers (companies) that publish models to the library,
with each provider's model count.

On a terminal this prints a table (NAME, MODELS). When stdout is not a
terminal it prints one provider per line as tab-separated values (name,
model_count) with no header. With --json the envelope {"results": [...],
"count": N} is emitted exactly as the API returned it.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.

```
melange library providers [flags]
```

### Examples

```
  # List providers
  melange library providers

  # Agent pattern: provider names with at least 10 models
  melange library providers --jq '.results[] | select(.model_count >= 10) | .name'

  # Machine-readable
  melange library providers --json
```

### Options

```
  -h, --help              help for providers
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

* [melange library](melange_library.md)	 - Browse the public model library

