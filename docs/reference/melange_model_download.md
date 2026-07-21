## melange model download

Download a converted target's artifacts (billable)

### Synopsis

Download the artifacts of a converted target. This is BILLABLE: the
target's download size counts against your account's bandwidth quota —
also for public models owned by others.

On a terminal the command previews the target and its size and asks for
confirmation before anything is charged; non-interactive runs (and
--no-input) require --yes instead. The authorization request carries an
Idempotency-Key, so transient failures are retried without a double
charge. Exceeding the quota is an error with nothing charged.

Files are written to --output (default: the current directory; an
existing directory receives one file per artifact, any other path names
the destination file for single-artifact targets). Each file is
downloaded to a temporary file, verified against the artifact's
checksum when one is available, and atomically renamed into place —
interrupted downloads never leave partial files. Existing files are
never overwritten without --force.

With --json the authorization response is written to stdout with every
artifact url replaced by "<redacted>" (the only documented deviation
from byte-exact --json): use this command to download, or melange api
if you genuinely need raw signed URLs.

Exit codes: 0 success, 1 API/download/verification error (including
quota exhaustion), 2 usage error or missing confirmation, 4 not
authenticated, 130 interrupted.

```
melange model download MODEL_KEY --target TARGET_ID [flags]
```

### Examples

```
  # Pick a target, then download it into a directory
  melange model targets m_ab12cd -R zetic/whisper-tiny
  melange model download m_ab12cd -R zetic/whisper-tiny --target tm_71 --output ./models

  # Agent pattern: non-interactive download (the billable step needs --yes)
  melange model download m_ab12cd -R zetic/whisper-tiny --target tm_71 --yes

  # Agent pattern: capture the authorization id (URLs are redacted)
  melange model download m_ab12cd -R zetic/whisper-tiny --target tm_71 --yes --json --jq .authorization_id
```

### Options

```
      --force               Overwrite existing files
  -h, --help                help for download
      --jq expression       Filter JSON output using a jq expression (implies --json)
      --json                Output the full result as JSON
  -o, --output directory    Destination directory (or file for single-artifact targets) (default ".")
  -R, --repo ACCOUNT/REPO   Repository as ACCOUNT/REPO (required)
      --target TARGET_ID    Target to download as TARGET_ID (see `melange model targets`)
      --template string     Format JSON output using a Go template (implies --json)
      --yes                 Skip the billable-download confirmation
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange model](melange_model.md)	 - Upload, browse, and download models

