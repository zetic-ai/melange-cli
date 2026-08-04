## melange plan

Show the account's billing plan

### Synopsis

Show the effective billing plan for the token's account: the tier its
quotas derive from (free, lite, pro, pro_plus, or enterprise), whether it is
a trial, and when a trial ends.

The plan reflects what the server actually enforces — an account that bypasses
quota limits reports pro_plus, matching the dashboard. Use "melange usage
quotas" for the per-counter headroom.

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (plan, is_trial,
trial_ends_at; trial_ends_at is empty when not a trial). With --json, API
fields and order are preserved and output ends with exactly one trailing
newline.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.

```
melange plan [flags]
```

### Examples

```
  # Show the plan
  melange plan

  # Machine-readable
  melange plan --json

  # Agent pattern: the plan tier
  melange plan --jq .plan
```

### Options

```
  -h, --help              help for plan
      --jq expression     Filter JSON output using a jq expression (implies --json)
      --json              Output the full result as JSON
      --template string   Format JSON output using a Go template (implies --json)
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
