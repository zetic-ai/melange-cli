## melange model download

Download a converted target's artifacts (billable)

### Synopsis

Download the artifacts of a converted target. This is BILLABLE: the
target's download size counts against your account's bandwidth quota —
also for public models owned by others.

On a terminal the command previews the target and its size and asks for
confirmation before anything is charged; non-interactive runs (and
--no-input) require --yes instead. The authorization request carries an
Idempotency-Key that is persisted in per-user application state and reused
by later CLI processes for the same host, account/repo, model, and target. Local output
is tracked separately so a post-authorization correction from file or
stdout to a directory keeps the charged key. A cross-process lock
serializes transfers; durable completion/recovery state prevents a
waiting or failed follower from rotating the key after another process
succeeds. The private state stores no signed URLs or access tokens.
Exceeding the quota is an error with nothing charged.

Files are written to --output (default: the current directory; an
existing directory receives one file per artifact, any other path names
the destination file for single-artifact targets). Each file is
downloaded to a temporary file, verified against the artifact's
checksum when one is available, and atomically committed into place —
interrupted downloads never leave partial files. Existing files are
never overwritten without --force. Connection resets, timeouts, 429
(honoring Retry-After), and HTTP 502–504 artifact failures are retried
with bounded backoff. An expired 403/404 signed URL is refreshed once
with the persisted authorization key. A transfer with no byte progress
for 30 seconds is canceled and retried; every received chunk resets that
inactivity timer.

The CLI validates the output path and known file collisions before the
billable request. Artifact names are only disclosed by that response, so
an existing same-named file inside an output directory is necessarily
detected afterward; replay state is kept and the error tells you to
re-run the same command with --force without another charge.

Set --output - for one artifact to write verified binary bytes to stdout.
The artifact is fully staged and verified before stdout is touched; this
mode cannot be combined with --json, --jq, or --template.

With --json the authorization response is written to stdout with every
artifact url replaced by "<redacted>" (the only documented deviation
from the API response). Output ends with exactly one trailing newline.
Use this command to download, or melange api if you genuinely need raw
signed URLs.

Exit codes: 0 success, 1 API/download/verification error (including
quota exhaustion), 2 usage error or missing confirmation, 4 not
authenticated, 130 interrupted.

```
melange model download MODEL_KEY --target TARGET_ID [flags]
```

### Examples

```
  # Resolve a model and one of its converted targets
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results[] | select(.is_default) | .key')
  target_id=$(melange model targets "$model_key" -R zetic/whisper-tiny --jq '.results[0].target_id')

  # Download the target into a directory
  melange model download "$model_key" -R zetic/whisper-tiny --target "$target_id" --output ./models

  # Agent pattern: non-interactive download (the billable step needs --yes)
  melange model download "$model_key" -R zetic/whisper-tiny --target "$target_id" --yes

  # Agent pattern: capture the authorization id (URLs are redacted)
  melange model download "$model_key" -R zetic/whisper-tiny --target "$target_id" --yes --json --jq .authorization_id
```

### Options

```
      --force               Overwrite existing files
  -h, --help                help for download
      --jq expression       Filter JSON output using a jq expression (implies --json)
      --json                Output the full result as JSON
  -o, --output directory    Destination directory, single-artifact file, or - for binary stdout (default ".")
  -R, --repo ACCOUNT/REPO   Repository as ACCOUNT/REPO (required)
      --target TARGET_ID    Target to download as TARGET_ID (see `melange model targets`)
      --template string     Format JSON output using a Go template (implies --json)
      --yes                 Skip the billable-download confirmation
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange model](melange_model.md)	 - Upload, browse, and download models
