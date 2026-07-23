## melange auth status

Show authentication status for the current host

### Synopsis

Verify the resolved token against the API and report the identity
behind it: host, account, token name, scopes, where the token came from
(env, keyring, or config), and where it is stored.

Exit codes: 0 authenticated, 1 network/API error, 4 not logged in or the
token was rejected.

```
melange auth status [flags]
```

### Examples

```
  # Human-readable status
  melange auth status

  # Agent pattern: verify a token non-interactively
  MELANGE_API_KEY=ztp_... melange auth status --json
```

### Options

```
  -h, --help              help for status
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

* [melange auth](melange_auth.md)	 - Authenticate melange with the Melange platform
