## melange repo list

List repositories

### Synopsis

List the repositories your token can see.

On a terminal this prints a table (REPO, VISIBILITY, TYPE, UPDATED).
When stdout is not a terminal it prints one repository per line as
tab-separated values (full_name, visibility, model_type, RFC 3339
updated_at) with no header — stable for scripts. With --json the API page
envelope {"results": [...], "count": N} is preserved and followed by exactly
one trailing newline; --paginate merges all pages into one envelope.

An empty result exits 0: a terminal gets "No repositories found" on
stderr, scripts get empty stdout, --json gets the envelope with empty
results.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.

```
melange repo list [flags]
```

### Examples

```
  # List your 30 most relevant repositories
  melange repo list

  # Search across every page of results
  melange repo list --search whisper --paginate

  # Agent pattern: just the repository names
  melange repo list --jq '.results[].full_name'
```

### Options

```
      --all               Alias for --paginate
  -h, --help              help for list
      --jq expression     Filter JSON output using a jq expression (implies --json)
      --json              Output the full result as JSON
      --limit int         Maximum number of repositories to fetch (1-100) (default 30)
      --paginate          Fetch all pages of results
      --search query      Filter repositories by a search query
      --template string   Format JSON output using a Go template (implies --json)
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange repo](melange_repo.md)	 - Manage model repositories
