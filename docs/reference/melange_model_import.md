## melange model import

Import an LLM from a public HuggingFace repository

### Synopsis

Register an LLM model from a public HuggingFace repository (for
example "meta-llama/Llama-3.2-1B"; hf:// and URL prefixes are accepted).

The target repository must have model type llm; other repositories are
rejected by the server. Conversion continues asynchronously — poll it
with "melange model status", or pass --wait to block until it reaches a
terminal state.

The request carries an Idempotency-Key, so transient failures are
retried safely; replaying the same import returns the original model.
Pinning a HuggingFace revision is not supported yet: imports always use
the repository's current default-branch head.

On success a confirmation with the model key, version, and state goes
to stderr; with --json the created model reference is written to stdout
exactly as the API returned it (with --wait, the final status is
emitted instead).

Exit codes: 0 success, 1 API error or failed conversion under --wait,
2 usage error, 4 not authenticated.

```
melange model import HF_REPO [flags]
```

### Examples

```
  # Import a model and wait for conversion
  melange model import meta-llama/Llama-3.2-1B -R zetic/llama --wait

  # Import without waiting
  melange model import meta-llama/Llama-3.2-1B -R zetic/llama

  # Agent pattern: capture the new model key
  melange model import meta-llama/Llama-3.2-1B -R zetic/llama --json --jq .key
```

### Options

```
  -h, --help                help for import
      --jq expression       Filter JSON output using a jq expression (implies --json)
      --json                Output the full result as JSON
  -R, --repo ACCOUNT/REPO   Target repository as ACCOUNT/REPO (required)
      --template string     Format JSON output using a Go template (implies --json)
      --timeout duration    Maximum time to wait with --wait (default 30m0s)
      --wait                After import, wait until conversion reaches a terminal state
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange model](melange_model.md)	 - Upload, browse, and download models

