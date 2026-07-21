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

--dry-run prints the manifest that would be uploaded — including the
destination layout — without any network calls.

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

  # Resume an interrupted upload
  melange model upload --resume up_ab12cd -R zetic/whisper-tiny

  # List and clean up sessions
  melange model upload --sessions -R zetic/whisper-tiny
  melange model upload --cancel up_ab12cd -R zetic/whisper-tiny
```

### Options

```
      --bucket INDEX:DIMS             Bucket dims as INDEX:DIMS (not yet supported)
      --cancel session                Cancel the upload session
      --dry-run                       Print the manifest without creating a session or uploading
      --external-data file            External data file, e.g. ONNX external weights (repeatable)
  -h, --help                          help for upload
      --inactivity-timeout duration   Per-chunk stall timeout during uploads (default 2m0s)
      --input file                    Sample input file (repeatable; order defines input_index)
      --input-manifest file           JSON manifest file describing all files (alternative to flags)
      --jq expression                 Filter JSON output using a jq expression (implies --json)
      --json                          Output the full result as JSON
  -R, --repo ACCOUNT/REPO             Target repository as ACCOUNT/REPO (required)
      --resume session                Resume the upload session
      --sessions                      List upload sessions for the repository
      --template string               Format JSON output using a Go template (implies --json)
      --timeout duration              Maximum time to wait with --wait (default 30m0s)
      --wait                          After upload, wait until conversion reaches a terminal state
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange model](melange_model.md)	 - Upload, browse, and download models

