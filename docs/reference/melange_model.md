## melange model

Upload, browse, and download models

### Synopsis

Work with the models of a Melange repository: upload new models,
follow their conversion, list and inspect them, browse their converted
targets, download target artifacts, import LLMs from HuggingFace, and
set the repository default.

An upload is a session: the CLI declares a manifest (model file, optional
inputs and external data, each with size and CRC32C), streams the bytes to
signed storage URLs with resumable uploads, then completes the session so
the server verifies integrity and starts conversion. Interrupted uploads
resume without re-sending acknowledged bytes.

Model commands always require an explicit -R ACCOUNT/REPO. Data is
written to stdout; progress and messages go to stderr.

### Examples

```
  # Upload a model and wait until it is ready
  melange model upload -R zetic/whisper-tiny model.onnx --wait

  # List models and inspect one
  melange model list -R zetic/whisper-tiny
  melange model view m_ab12cd -R zetic/whisper-tiny

  # Download a converted target (billable)
  melange model targets m_ab12cd -R zetic/whisper-tiny
  melange model download m_ab12cd -R zetic/whisper-tiny --target tm_71

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
* [melange model download](melange_model_download.md)	 - Download a converted target's artifacts (billable)
* [melange model import](melange_model_import.md)	 - Import an LLM from a public HuggingFace repository
* [melange model list](melange_model_list.md)	 - List models in a repository
* [melange model set-default](melange_model_set-default.md)	 - Make a model the repository default
* [melange model status](melange_model_status.md)	 - Show a model's conversion status
* [melange model targets](melange_model_targets.md)	 - List a model's converted targets
* [melange model upload](melange_model_upload.md)	 - Upload a model to a repository
* [melange model view](melange_model_view.md)	 - View a model

