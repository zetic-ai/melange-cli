## melange auth logout

Remove stored credentials for the current host

### Synopsis

Delete the token stored for the resolved host from both the OS keyring
and the config file. Environment variables are not touched: if
MELANGE_API_KEY or MELANGE_API_KEY_FILE is set, it still takes precedence
and a note is printed.

Exit codes: 0 success, 1 storage error.

```
melange auth logout [flags]
```

### Examples

```
  # Log out of the default host
  melange auth logout

  # Log out of a specific host
  MELANGE_HOST=api.staging.zetic.ai melange auth logout
```

### Options

```
  -h, --help   help for logout
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange auth](melange_auth.md)	 - Authenticate melange with the Melange platform
