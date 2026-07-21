## melange library view

View a library model

### Synopsis

Show a single public library model: its full name, provider, use-case
task, model type, tags, description, and a readme excerpt (the first 10
lines, noting when it is truncated).

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (full_name,
account, name, provider, use_case, model_type, tags as comma-joined,
description, RFC 3339 created_at; readme omitted). With --json the
resource object — including the full readme — is emitted exactly as the
API returned it.

Exit codes: 0 success, 1 API error (including not found), 2 usage error,
4 not authenticated.

```
melange library view ACCOUNT/NAME [flags]
```

### Examples

```
  # View a library model
  melange library view zetic/whisper-tiny

  # The full readme as JSON
  melange library view zetic/whisper-tiny --jq .readme

  # Machine-readable detail
  melange library view zetic/whisper-tiny --json
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
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange library](melange_library.md)	 - Browse the public model library

