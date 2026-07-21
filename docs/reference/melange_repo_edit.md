## melange repo edit

Edit a repository

### Synopsis

Edit a repository's metadata: description, visibility, use case, and
tags.

Only the fields you pass are changed (the PATCH body carries exactly the
provided fields): --description "" clears the description, and --tag
replaces the complete tag set atomically — repeat it once per tag you
want the repository to end up with.

Changing --visibility is restricted to the repository owner server-side;
other members get the server's 403 message.

On success a confirmation goes to stderr and stdout stays empty; with
--json the updated resource object is written to stdout exactly as the
API returned it.

Exit codes: 0 updated, 1 API error (including 403 permission and 404
not found), 2 usage error, 4 not authenticated.

```
melange repo edit <[account/]name> [flags]
```

### Examples

```
  # Update the description
  melange repo edit zetic/whisper-tiny --description "Tiny Whisper for on-device ASR"

  # Make a repository public and replace its tags
  melange repo edit zetic/whisper-tiny --visibility public --tag asr --tag tiny

  # Agent pattern: edit and confirm the resulting visibility
  melange repo edit zetic/whisper-tiny --visibility private --json --jq .is_private
```

### Options

```
      --description string   New description ("" clears it)
  -h, --help                 help for edit
      --jq expression        Filter JSON output using a jq expression (implies --json)
      --json                 Output the full result as JSON
      --tag stringArray      Replacement tag (repeatable; replaces the whole tag set)
      --template string      Format JSON output using a Go template (implies --json)
      --use-case string      Use case: {vision|nlp|llm|speech|other}
      --visibility string    Visibility: {public|private} (owner only)
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange repo](melange_repo.md)	 - Manage model repositories

