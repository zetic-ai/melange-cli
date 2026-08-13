## melange plan

Show the account's billing plan

### Synopsis

Show the effective billing identity for the token's account. Two
vocabularies coexist:

"plan" is the legacy tier the account's quotas derive from (free, lite,
pro, pro_plus, or enterprise). It reflects what the server actually
enforces — an account that bypasses quota limits reports pro_plus,
matching the dashboard. Use "melange usage quotas" for the per-counter
headroom.

"tier" is the current pricing identity (free, pro, team, or enterprise).
It is null on accounts still on legacy billing; "billing_generation" says
which system governs the account (legacy or v3).

"max_model_bytes" is the plan's own cap on a custom model's total bytes.
It preflights only that size entitlement — other billing checks (credits,
debt, subscription state) are enforced separately at conversion time. A
null cap means a custom contract, NOT an unlimited one: the credit ledger
still refuses runs above the self-service size ceiling.

On a terminal this prints a human-readable block; every field is always
shown, and one the account does not carry renders as "-". When stdout is
not a terminal it prints stable tab-separated key/value lines (plan, is_trial,
trial_ends_at, billing_generation, tier, max_model_bytes; trial_ends_at
is empty when not a trial, tier and max_model_bytes are empty when null).
With --json, API fields and order are preserved and output ends with
exactly one trailing newline.

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

  # Agent pattern: the legacy plan tier
  melange plan --jq .plan

  # Agent pattern: the pricing identity (null on legacy billing)
  melange plan --jq .tier
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
