## melange model set-default

Make a model the repository default

### Synopsis

Make this model the repository's default. Exactly one model per
repository is the default; setting a new one clears the previous.

The operation is idempotent: repeating it returns the same result.

On success a confirmation goes to stderr and stdout stays empty; with
--json the resulting model summary is written to stdout exactly as the
API returned it.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.

```
melange model set-default MODEL_KEY [flags]
```

### Examples

```
  # Select a model key from the repository
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results[0].key')

  # Set that model as the default
  melange model set-default "$model_key" -R zetic/whisper-tiny

  # Machine-readable result
  melange model set-default "$model_key" -R zetic/whisper-tiny --json

  # Agent pattern: confirm the default flag stuck
  melange model set-default "$model_key" -R zetic/whisper-tiny --json --jq .is_default
```

### Options

```
  -h, --help                help for set-default
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

