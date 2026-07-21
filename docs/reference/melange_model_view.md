## melange model view

View a model

### Synopsis

Show a single model: key, version, type, state, whether it is the
repository's default, its source (upload or import), the terminal and
download-ready flags, a sanitized failure code when processing failed,
and timestamps.

On a terminal this prints a human-readable block. When stdout is not a
terminal it prints stable tab-separated key/value lines (key, version,
type, state, is_default, source_type, terminal, download_ready,
failure_code when present, created_at, updated_at; timestamps in
RFC 3339). With --json the resource object is emitted exactly as the
API returned it.

Exit codes: 0 success, 1 API error (including not found), 2 usage
error, 4 not authenticated.

```
melange model view MODEL_KEY [flags]
```

### Examples

```
  # View a model
  melange model view m_ab12cd -R zetic/whisper-tiny

  # Machine-readable detail
  melange model view m_ab12cd -R zetic/whisper-tiny --json

  # Agent pattern: is the model downloadable yet?
  melange model view m_ab12cd -R zetic/whisper-tiny --jq .download_ready
```

### Options

```
  -h, --help                help for view
      --jq expression       Filter JSON output using a jq expression (implies --json)
      --json                Output the full result as JSON
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

