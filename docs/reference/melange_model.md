## melange model

Upload models and track their conversion

### Synopsis

Upload models to a Melange repository and follow their conversion.

An upload is a session: the CLI declares a manifest (model file, optional
inputs and external data, each with size and CRC32C), streams the bytes to
signed storage URLs with resumable uploads, then completes the session so
the server verifies integrity and starts conversion. Interrupted uploads
resume without re-sending acknowledged bytes.

Uploads always require an explicit -R ACCOUNT/REPO. Data is written to
stdout; progress and messages go to stderr.

### Examples

```
  # Upload a model and wait until it is ready
  melange model upload -R zetic/whisper-tiny model.onnx --wait

  # Upload with sample inputs (order defines input_index)
  melange model upload -R zetic/whisper-tiny model.onnx --input audio.bin --input mask.bin

  # Preview the manifest without creating anything
  melange model upload -R zetic/whisper-tiny model.onnx --dry-run

  # Check conversion status
  melange model status m_ab12cd -R zetic/whisper-tiny
```

### Options

```
  -h, --help   help for model
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
* [melange model status](melange_model_status.md)	 - Show a model's conversion status
* [melange model upload](melange_model_upload.md)	 - Upload a model to a repository

