## melange model upload

Upload a model to a repository

### Synopsis

Upload a model (with optional sample inputs and external data) to a
repository through a resumable upload session.

The flow: a manifest with per-file sizes and CRC32C checksums opens a
session; bytes stream to signed storage URLs with resumable uploads; the
session is completed, the server verifies every checksum, registers the
model, and starts conversion. --wait then polls conversion status until
the model is ready.

-R ACCOUNT/REPO is always required — uploads never fall back to a default
repository. Interrupting an upload (Ctrl-C) preserves the session; resume
it with --resume SESSION_ID (already-uploaded bytes are never re-sent) or
discard it with --cancel SESSION_ID. --sessions lists sessions.
Once the server reports VERIFYING, DISPATCH_PENDING, or CONVERTING,
--resume replays completion and no longer needs the original local files.
Cancellation prompts for the exact session ID on a terminal; agents,
non-interactive runs, and --no-input must pass --yes explicitly.

When a signed upload URL expires, reissue intentionally mints a fresh URL
and carries no Idempotency-Key; create, complete, and cancel retain their
documented idempotency keys.

--dry-run prints the manifest that would be uploaded — including the
destination layout — without any network calls.

For a bucketed .pt2 model, repeat --bucket in declaration order and pass
one complete group of --input files for each bucket. For example, two
buckets and four inputs means two inputs per bucket: the first two inputs
belong to the first bucket and the next two to the second. Within every
bucket, input order defines input_index.

--input-manifest accepts this CLI-private local-file shape (it is not the
public API wire manifest):
  {
    "manifest_version": 2,
    "files": [
      {"path": "models/model.onnx", "role": "model"},
      {"path": "samples/audio.npy", "role": "input", "input_index": 0}
    ]
  }
path is required and is resolved relative to the manifest file. filename
is optional. input_index is optional for inputs and defaults by order;
bucket_index is valid for inputs when options.buckets is declared.

With --wait, structured output is
{"model": <created model>, "status": <final status>}; for example,
--jq .model.key returns the model key. Without --wait, --json remains the
raw upload-complete response.

Exit codes: 0 success, 1 upload/verification/conversion failure, 2 usage
error, 4 not authenticated, 130 interrupted (session preserved).

```
melange model upload MODEL_FILE [flags]
```

### Examples

```
  # Upload a model with two sample inputs and wait for conversion
  melange model upload -R zetic/whisper-tiny model.onnx --input audio.bin --input mask.bin --wait

  # Preview the manifest without uploading
  melange model upload -R zetic/whisper-tiny model.onnx --dry-run

  # A model-only manifest is valid; wait and print its stable model key
  melange model upload -R zetic/whisper-tiny model.onnx --wait --jq .model.key

  # Upload a .pt2 model with two shape buckets and two inputs per bucket
  melange model upload -R zetic/vision model.pt2 \
    --bucket 0:1x3x224x224 --input image-224.npy --input mask-224.npy \
    --bucket 1:1x3x384x384 --input image-384.npy --input mask-384.npy --wait

  # Resolve a resumable pre-completion session id

  session_id=$(melange model upload --sessions -R zetic/whisper-tiny --jq '.results | map(select(.state=="CREATED" or .state=="UPLOADING")) | first | .id // empty')

  # Do not accidentally send the literal JSON value null as an id
  [ -n "$session_id" ] || { echo "No resumable upload session found" >&2; exit 1; }

  # Resume it
  melange model upload --resume "$session_id" -R zetic/whisper-tiny

  # Or cancel it instead (scripts must opt in explicitly)
  melange model upload --cancel "$session_id" -R zetic/whisper-tiny --yes
```

### Options

```
      --bucket .pt2                   .pt2 bucket as `INDEX:DIMS` (repeatable; group --input files by bucket order)
      --cancel session                Cancel the upload session
      --dry-run                       Print the manifest without creating a session or uploading
      --external-data file            External data file, e.g. ONNX external weights (repeatable)
  -h, --help                          help for upload
      --inactivity-timeout duration   Per-chunk stall timeout during uploads (default 2m0s)
      --input file                    Sample input file (repeatable; order defines input_index)
      --input-manifest file           CLI-local JSON manifest file describing all files (alternative to flags)
      --jq expression                 Filter JSON output using a jq expression (implies --json)
      --json                          Output the full result as JSON
  -R, --repo ACCOUNT/REPO             Target repository as ACCOUNT/REPO (required)
      --resume session                Resume the upload session
      --sessions                      List upload sessions for the repository
      --template string               Format JSON output using a Go template (implies --json)
      --timeout duration              Maximum time to wait with --wait (default 30m0s)
      --wait                          After upload, wait until conversion reaches a terminal state
  -y, --yes                           Confirm upload-session cancellation without prompting
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange model](melange_model.md)	 - Upload, browse, and download models
