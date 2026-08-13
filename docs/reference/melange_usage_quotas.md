## melange usage quotas

Show usage against plan limits

### Synopsis

Show your current-period usage against your plan limits: active
devices, bandwidth, model uploads, and prompts, plus the account's
benchmark-credit balance.

Each quota renders as "used/limit (pct%)"; a null limit renders as
"unlimited". On a terminal this prints a human-readable block. When
stdout is not a terminal it prints stable tab-separated key/value lines
(each value the same "used/limit (pct%)" or "unlimited" string, followed
by credits_available, credits_reserved, credits_outstanding_debt,
credits_monthly_credits, credits_expiring_credits, and credits_expiring_at
lines; nullable values are empty when null). With --json, API fields and
order are preserved and output ends with exactly one trailing newline.

Each counter also carries a "remaining" field in --json: the amount the
server would actually allow right now (spike headroom included, floored at
0; null means unlimited). Prefer "remaining" over deriving limit-used for
preflight checks — it reflects what enforcement permits.

The "credits" balance is ADVISORY: "model_uploads.remaining > 0" and
"credits.available > 0" are necessary but not sufficient for the next
conversion — "credits.outstanding_debt" must also be 0, and the
per-conversion charge grows with the model's size. When the balance cannot
cover the charge, the conversion is refused with HTTP 402
"credit_balance_exhausted" (nothing is charged and an upload session stays
resumable).

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

  # Agent pattern: advisory conversion preflight (headroom, credits, no debt)
  melange usage quotas --jq '{uploads: .model_uploads.remaining,
    credits: .credits.available, debt: .credits.outstanding_debt}'
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
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange usage](melange_usage.md)	 - Show current usage counters
