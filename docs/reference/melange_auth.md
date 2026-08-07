## melange auth

Authenticate melange with the Melange platform

### Synopsis

Manage authentication for the Melange API.

By default opens a browser for OAuth (recommended); use --with-token for personal access tokens (CI/headless).
Credentials are resolved in this order:
MELANGE_API_KEY > MELANGE_API_KEY_FILE > OAuth (keyring/config, auto-refreshed) > PAT keyring > PAT config fallback.

```
melange auth <command> [flags]
```

### Examples

```
  # Browser login (recommended)
  melange auth login

  # Log in from a file, for scripts and agents
  melange auth login --with-token < token.txt

  # Check who you are, as JSON
  MELANGE_API_KEY=ztp_... melange auth status --json
```

### Options

```
  -h, --help   help for auth
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
* [melange auth login](melange_auth_login.md)	 - Log in to the Melange platform
* [melange auth logout](melange_auth_logout.md)	 - Remove stored credentials for the current host
* [melange auth status](melange_auth_status.md)	 - Show authentication status for the current host
* [melange auth token](melange_auth_token.md)	 - Print the resolved authentication token
