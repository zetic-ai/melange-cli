## melange model status

Show a model's conversion status

### Synopsis

Show the conversion status of a model: state (converting, optimizing,
ready, failed), the pipeline stage, whether the model is downloadable,
and a sanitized failure code when processing failed.

A plain status read always exits 0 — it is a query. With --wait the
command polls until a terminal state and the exit code reflects the
outcome: 0 when the model is ready, 1 when processing failed or --timeout
elapsed.

On a terminal a human summary is printed; otherwise stable tab-separated
key/value lines. --json preserves API fields and order and adds exactly one
trailing newline.

Exit codes: 0 success, 1 failed outcome under --wait or API error, 2 usage
error, 4 not authenticated.

```
melange model status MODEL_KEY [flags]
```

### Examples

```
  # Resolve the default model key
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results[] | select(.is_default) | .key')

  # Check status once
  melange model status "$model_key" -R zetic/whisper-tiny

  # Block until conversion finishes (up to --timeout)
  melange model status "$model_key" -R zetic/whisper-tiny --wait

  # Agent pattern: just the state
  melange model status "$model_key" -R zetic/whisper-tiny --jq .state
```

### Options

```
  -h, --help                help for status
      --jq expression       Filter JSON output using a jq expression (implies --json)
      --json                Output the full result as JSON
  -R, --repo ACCOUNT/REPO   Repository as ACCOUNT/REPO (required)
      --template string     Format JSON output using a Go template (implies --json)
      --timeout duration    Maximum time to wait with --wait (default 30m0s)
      --wait                Poll until the model reaches a terminal state
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange model](melange_model.md)	 - Upload, browse, and download models
