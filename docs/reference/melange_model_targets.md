## melange model targets

List a model's converted targets

### Synopsis

List the converted target artifacts of a model, newest first. Each
target is identified by an opaque, stable TARGET_ID — pass it to
"melange model download --target".

On a terminal this prints a table (TARGET_ID, KIND, TARGET, QUANT,
COMPATIBILITY, SIZE) with human-readable sizes; COMPATIBILITY is a
compact soc/os string, or "-" when the target carries no device
compatibility (LLM targets). When stdout is not a terminal, rows are
tab-separated with sizes in raw bytes and no header. With --json, all API
target metadata is preserved and output ends with exactly one trailing
newline.

Exit codes: 0 success, 1 API error, 2 usage error, 4 not authenticated.

```
melange model targets MODEL_KEY [flags]
```

### Examples

```
  # Resolve the default model key
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results[] | select(.is_default) | .key')

  # List that model's targets
  melange model targets "$model_key" -R zetic/whisper-tiny

  # Full detail including the compatibility object
  melange model targets "$model_key" -R zetic/whisper-tiny --json

  # Agent pattern: pick the target id for a quant type
  melange model targets "$model_key" -R zetic/whisper-tiny --jq '.results[] | select(.quant_type == "q4_k_m") | .target_id'
```

### Options

```
  -h, --help                help for targets
      --jq expression       Filter JSON output using a jq expression (implies --json)
      --json                Output the full result as JSON
  -R, --repo ACCOUNT/REPO   Repository as ACCOUNT/REPO (required)
      --template string     Format JSON output using a Go template (implies --json)
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange model](melange_model.md)	 - Upload, browse, and download models
