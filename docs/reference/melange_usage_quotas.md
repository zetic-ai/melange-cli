## melange usage quotas

Show usage against plan limits

### Synopsis

Show your current-period usage against your plan limits: active
devices, bandwidth, model uploads, and prompts.

Each quota renders as "used/limit (pct%)"; a null limit renders as
"unlimited". On a terminal this prints a human-readable block. When
stdout is not a terminal it prints stable tab-separated key/value lines
(each value the same "used/limit (pct%)" or "unlimited" string). With
--json, API fields and order are preserved and output ends with exactly one
trailing newline.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.

```
melange usage quotas [flags]
```

### Examples

```
  # Show quotas
  melange usage quotas

  # Machine-readable
  melange usage quotas --json

  # Agent pattern: the prompts limit (null when unlimited)
  melange usage quotas --jq .prompts.limit
```

### Options

```
  -h, --help              help for quotas
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

* [melange usage](melange_usage.md)	 - Show current usage counters
