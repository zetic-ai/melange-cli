## melange library list

List public library models

### Synopsis

List models in the public library, filtered by task, search text, or
provider.

--task may be repeated; a model matching ANY given task is included
(vision, llm, nlp, speech, other). --search is a case- and separator-insensitive
substring match on name or full_name (hyphens, underscores, slashes, and spaces
are ignored). --provider is an exact provider name.

On a terminal this prints a table (MODEL, PROVIDER, TASK, TYPE, CREATED).
When stdout is not a terminal it prints one model per line as tab-separated
values (full_name, provider, use_case, model_type, RFC 3339 created_at)
with no header — stable for scripts. With --json the API page envelope
{"results": [...], "count": N} is preserved and followed by exactly one
trailing newline; --paginate merges all pages into one envelope.

An empty result exits 0: a terminal gets "No models found" on stderr,
scripts get empty stdout, --json gets the envelope with empty results.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.

```
melange library list [flags]
```

### Examples

```
  # Speech models from a provider
  melange library list --task speech --provider Zetic

  # Search across every page
  melange library list --search whisper --paginate

  # Agent pattern: just the full names
  melange library list --jq '.results[].full_name'
```

### Options

```
      --all                        Alias for --paginate
  -h, --help                       help for list
      --jq expression              Filter JSON output using a jq expression (implies --json)
      --json                       Output the full result as JSON
      --limit int                  Maximum number of models to fetch (1-100) (default 30)
      --paginate                   Fetch all pages of results
      --provider name              Exact provider name
      --search name or full_name   Case- and separator-insensitive substring match on name or full_name
      --task task                  Filter by use-case task (repeatable): vision, llm, nlp, speech, other
      --template string            Format JSON output using a Go template (implies --json)
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange library](melange_library.md)	 - Browse the public model library
