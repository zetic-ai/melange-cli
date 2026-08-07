## melange auth login

Log in to the Melange platform

### Synopsis

Authenticate with the Melange platform.

By default opens a browser for OAuth (recommended); use --with-token for personal access tokens (CI/headless).

The token is verified against the API, then stored in the OS keyring.
If the keyring is unavailable, pass --insecure-storage to store it in the
config file (created with 0600 permissions) instead.

Interactive token input is hidden. Non-interactive runs must use --with-token
or set MELANGE_API_KEY.

Exit codes: 0 success, 1 storage or validation error, 2 usage error
(non-interactive without --with-token), 4 token rejected by the API.

```
melange auth login [flags]
```

### Examples

```
  # Browser login (recommended)
  melange auth login

  # Headless / SSH without browser
  melange auth login --no-browser

  # Scripted login for agents and CI
  melange auth login --with-token < token.txt

  # Machine-readable result
  melange auth login --with-token --json < token.txt
```

### Options

```
  -h, --help               help for login
      --insecure-storage   Store the token in the config file when the OS keyring is unavailable
      --jq expression      Filter JSON output using a jq expression (implies --json)
      --json               Output the full result as JSON
      --no-browser         Print the authorize URL and wait for callback without opening a browser (SSH/headless)
      --template string    Format JSON output using a Go template (implies --json)
      --with-token         Read a personal access token (ztp_) from standard input (CI/headless)
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange auth](melange_auth.md)	 - Authenticate melange with the Melange platform
