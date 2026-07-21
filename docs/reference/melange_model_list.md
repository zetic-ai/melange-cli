## melange model list

List models in a repository

### Synopsis

List the models of a repository, newest first.

On a terminal this prints a table (KEY, VERSION, TYPE, STATE, DEFAULT,
CREATED) with the state colored (ready green, failed red) and ✓ marking
the repository's default model. When stdout is not a terminal it prints
one model per line as tab-separated values (key, version, type, state,
is_default as true/false, RFC 3339 created_at) with no header — stable
for scripts. With --json the page envelope {"results": [...], "count": N}
is emitted exactly as the API returned it; --paginate merges all pages
into one envelope.

An empty result exits 0: a terminal gets "No models found" on stderr,
scripts get empty stdout, --json gets the envelope with empty results.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.

```
melange model list [flags]
```

### Examples

```
  # List the models of a repository
  melange model list -R zetic/whisper-tiny

  # Fetch every page as JSON
  melange model list -R zetic/whisper-tiny --paginate --json

  # Agent pattern: the key of the default model
  melange model list -R zetic/whisper-tiny --jq '.results[] | select(.is_default) | .key'
```

### Options

```
      --all                 Alias for --paginate
  -h, --help                help for list
      --jq expression       Filter JSON output using a jq expression (implies --json)
      --json                Output the full result as JSON
      --limit int           Maximum number of models to fetch (default 30)
      --paginate            Fetch all pages of results
  -R, --repo ACCOUNT/REPO   Repository as ACCOUNT/REPO (required)
      --template string     Format JSON output using a Go template (implies --json)
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange model](melange_model.md)	 - Upload, browse, and download models

