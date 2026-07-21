## melange repo

Manage model repositories

### Synopsis

Work with Melange model repositories: list the repositories your token
can see, inspect a single repository, and create new ones.

Repositories are addressed as ACCOUNT/NAME. Where the account is omitted,
it resolves to the account behind your token (one extra /v1/me call).

Data is written to stdout; progress and messages go to stderr. All
subcommands support --json, --jq, and --template for structured output.

### Examples

```
  # List your repositories
  melange repo list

  # Inspect one repository
  melange repo view zetic/whisper-tiny

  # Create a private repository
  melange repo create whisper-tiny --private
```

### Options

```
  -h, --help   help for repo
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
* [melange repo create](melange_repo_create.md)	 - Create a repository
* [melange repo list](melange_repo_list.md)	 - List repositories
* [melange repo view](melange_repo_view.md)	 - View a repository

