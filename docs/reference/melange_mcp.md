## melange mcp

Serve the Melange MCP server

### Synopsis

Serve the Melange MCP (Model Context Protocol) server so agent clients
can call Melange tools directly.

The process speaks MCP on stdin/stdout: stdout carries protocol frames
only, and diagnostics go to stderr. Credentials are resolved lazily on the
first tool call (MELANGE_API_KEY or melange auth login), so the server
starts even when logged out and reports authentication errors per tool
call instead of exiting.

--transport currently accepts only "stdio"; "http" arrives later.

Exit codes: 0 clean disconnect, 2 usage error, 130 interrupted.

```
melange mcp [flags]
```

### Examples

```
  # Register with Claude Code
  claude mcp add melange -- melange mcp

  # Serve on stdio (the default transport)
  melange mcp
```

### Options

```
  -h, --help               help for mcp
      --transport string   Transport to serve on: "stdio" (later: "http") (default "stdio")
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
