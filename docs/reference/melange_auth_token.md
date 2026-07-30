## melange auth token

Print the resolved authentication token

### Synopsis

Print the raw token that melange would use for the current host,
followed by a newline. Nothing else is written to stdout, so the output is
safe to pipe into other tools.

Exit codes: 0 token printed, 4 no token found.

```
melange auth token [flags]
```

### Examples

```
  # Reuse the stored token in a curl call
  curl -H "Authorization: Bearer $(melange auth token)" https://api.zetic.ai/v1/me

  # Export it for a child process
  export MELANGE_API_KEY="$(melange auth token)"
```

### Options

```
  -h, --help   help for token
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange auth](melange_auth.md)	 - Authenticate melange with the Melange platform
