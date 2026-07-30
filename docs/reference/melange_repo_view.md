## melange repo view

View a repository

### Synopsis

Show a single repository: name, visibility, type, use case, tags,
description, and timestamps.

When ACCOUNT/ is omitted, the repository is looked up under the account
behind your token (one extra /v1/me call).

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (name,
visibility, type, use_case, tags, description, created_at, updated_at;
timestamps in RFC 3339). With --json, API fields and order are preserved and
output ends with exactly one trailing newline.

Exit codes: 0 success, 1 API error (including not found), 2 usage
error, 4 not authenticated.

```
melange repo view <[account/]name> [flags]
```

### Examples

```
  # View a repository in your account
  melange repo view whisper-tiny

  # View another account's repository
  melange repo view zetic/whisper-tiny

  # Agent pattern: check whether a repository is private
  melange repo view zetic/whisper-tiny --jq .is_private
```

### Options

```
  -h, --help              help for view
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

* [melange repo](melange_repo.md)	 - Manage model repositories
