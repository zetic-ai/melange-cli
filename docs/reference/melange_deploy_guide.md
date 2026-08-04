## melange deploy guide

Print exact SDK deployment code for a model

### Synopsis

Render the SDK install and inference code for one contained model
version. Select a language and inference mode explicitly, or use the dashboard
defaults (android-kotlin and auto).

The guide always contains YOUR_PERSONAL_KEY. For credential safety this command
does not interpolate, print, or persist the active PAT. General-model tensor
construction remains an explicit TODO because tensor shapes and preprocessing
are model-specific.

```
melange deploy guide MODEL_KEY [flags]
```

### Examples

```
  # Android Kotlin, automatic target selection
  melange deploy guide MODEL_KEY -R ACCOUNT/REPO

  # iOS Swift, prefer speed
  melange deploy guide MODEL_KEY -R ACCOUNT/REPO --language ios-swift --mode speed

  # Structured guide for an agent
  melange deploy guide MODEL_KEY -R ACCOUNT/REPO --language flutter --mode accuracy --json
```

### Options

```
  -h, --help                help for guide
      --jq expression       Filter JSON output using a jq expression (implies --json)
      --json                Output the full result as JSON
      --language string     SDK language: android-kotlin, android-java, ios-swift, or flutter (default "android-kotlin")
      --mode string         Inference mode: auto, speed, or accuracy (default "auto")
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

* [melange deploy](melange_deploy.md)	 - Get SDK deployment code for a model
