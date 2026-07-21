## melange repo delete

Delete a repository

### Synopsis

Delete a repository and everything in it. This cannot be undone.

The full ACCOUNT/NAME is always required — a bare name is never resolved
against your own account for destructive commands.

On a terminal you are asked to type the full ACCOUNT/NAME to confirm.
Non-interactively (or with --no-input) the prompt is replaced by
--confirm ACCOUNT/NAME, which must match the argument exactly.

Deleting a repository is restricted to the repository owner server-side.

Exit codes: 0 deleted, 1 API error or rejected confirmation, 2 usage
error (including missing/mismatched --confirm), 4 not authenticated.

```
melange repo delete <account/name> [flags]
```

### Examples

```
  # Delete interactively (type the full name at the prompt)
  melange repo delete zetic/whisper-tiny

  # Agent pattern: delete without a prompt
  melange repo delete zetic/whisper-tiny --confirm zetic/whisper-tiny

  # Verify it is gone (exits 1 once deleted)
  melange repo view zetic/whisper-tiny --json
```

### Options

```
      --confirm ACCOUNT/NAME   Confirm deletion by repeating the full ACCOUNT/NAME (required when not a terminal)
  -h, --help                   help for delete
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange repo](melange_repo.md)	 - Manage model repositories

