## melange

melange — on-device AI model deployment & benchmarking

### Synopsis

melange is the command-line interface for the Zetic.ai Melange platform,
which lets you deploy, benchmark, and manage on-device AI models.

Authenticate by setting MELANGE_API_KEY or by running melange auth login.
Data is written to stdout; progress and diagnostics go to stderr.
Exit codes: 0 success, 1 error, 2 usage/flag error, 4 auth error, 130 interrupted.

Reference topics: melange help environment, melange help exit-codes,
melange help formatting.

### Examples

```
  # List repositories as JSON
  melange repo list --json

  # Upload a model and wait for conversion
  melange model upload -R acme/whisper model.onnx --input x.npy --wait

  # Call any API endpoint and extract a value
  melange api /v1/me --jq .account.name
```

### Options

```
  -h, --help       help for melange
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange api](melange_api.md)	 - Make an authenticated Melange API request
* [melange auth](melange_auth.md)	 - Authenticate melange with the Melange platform
* [melange deploy](melange_deploy.md)	 - Get SDK deployment code for a model
* [melange library](melange_library.md)	 - Browse the public model library
* [melange mcp](melange_mcp.md)	 - Serve the Melange MCP server
* [melange model](melange_model.md)	 - Upload, browse, and download models
* [melange plan](melange_plan.md)	 - Show the account's billing plan
* [melange repo](melange_repo.md)	 - Manage model repositories
* [melange report](melange_report.md)	 - Read model benchmark reports
* [melange usage](melange_usage.md)	 - Show current usage counters
* [melange version](melange_version.md)	 - Print the melange CLI version
