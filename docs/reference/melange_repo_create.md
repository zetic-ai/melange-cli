## melange repo create

Create a repository

### Synopsis

Create a repository in your account (the account behind your token).

On success a confirmation goes to stderr and stdout stays empty; with
--json the created resource object is written to stdout exactly as the
API returned it.

Creating repositories requires a token with the write scope.

Exit codes: 0 created, 1 API error (including a 409 name conflict and
403 missing scope), 2 usage error, 4 not authenticated.

```
melange repo create <name> [flags]
```

### Examples

```
  # Create a public repository
  melange repo create whisper-tiny

  # Create a private LLM repository with metadata
  melange repo create phi-mini --private --model-type llm --description "Phi for mobile" --tag llm --tag mobile

  # Agent pattern: create and capture the full name
  melange repo create whisper-tiny --json --jq .full_name
```

### Options

```
      --description string   Repository description
  -h, --help                 help for create
      --jq expression        Filter JSON output using a jq expression (implies --json)
      --json                 Output the full result as JSON
      --model-type string    Model type: {general|llm} (default "general")
      --private              Make the repository private
      --tag stringArray      Add a tag (repeatable)
      --template string      Format JSON output using a Go template (implies --json)
      --use-case string      Use case: {vision|nlp|llm|speech|other}
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange repo](melange_repo.md)	 - Manage model repositories

