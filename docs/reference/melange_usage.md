## melange usage

Show current usage counters

### Synopsis

Show your usage for the current billing period: active devices,
bandwidth, model uploads, and prompts.

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (active_devices,
bandwidth, model_uploads, prompts). With --json, API fields and order are
preserved and output ends with exactly one trailing newline. Use "melange
usage quotas" to see these counters against your plan limits.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.

```
melange usage [flags]
```

### Examples

```
  # Show usage
  melange usage

  # Machine-readable
  melange usage --json

  # Agent pattern: prompts used this period
  melange usage --jq .prompts
```

### Options

```
  -h, --help              help for usage
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

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
* [melange usage quotas](melange_usage_quotas.md)	 - Show usage against plan limits
