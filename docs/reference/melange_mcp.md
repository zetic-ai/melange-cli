## melange mcp

Serve the Melange MCP server

### Synopsis

Serve the Melange MCP (Model Context Protocol) server so agent clients
can call Melange tools directly.

stdio (the default) speaks MCP on stdin/stdout: stdout carries protocol
frames only, and diagnostics go to stderr. Credentials are resolved lazily
on the first tool call (MELANGE_API_KEY or melange auth login), so the
server starts even when logged out and reports authentication errors per
tool call instead of exiting.

http serves the MCP Streamable HTTP transport on --listen for remote agent
clients. Every request must carry its own credential as
"Authorization: Bearer <personal access token>": the server itself has no
credentials and never reads the local keyring or MELANGE_API_KEY, so one
deployment serves many callers without sharing a token. Requests are
stateless (no session ids), GET /healthz is an unauthenticated liveness
probe, and browser Origins are rejected unless listed in --allowed-origins.
Only API-backed tools are served: anything that would touch the caller's own
machine (model uploads) stays stdio-only, because the server cannot see the
caller's files.

Exit codes: 0 clean disconnect (stdio) or completed drain after SIGINT or
SIGTERM (http), 1 serve failure such as an address already in use or a drain
that overran its deadline, 2 usage error, 130 interrupted (stdio).

```
melange mcp [flags]
```

### Examples

```
  # Register with Claude Code
  claude mcp add melange -- melange mcp

  # Serve on stdio (the default transport)
  melange mcp

  # Serve remote agent clients over HTTP; each request brings its own token
  melange mcp --transport http --listen 0.0.0.0:8080
```

### Options

```
      --allowed-origins strings   With --transport http, browser Origins allowed to call the server (empty rejects all)
  -h, --help                      help for mcp
      --listen string             Address to listen on with --transport http (use 0.0.0.0:PORT in a container) (default "127.0.0.1:8080")
      --transport string          Transport to serve on: "stdio" or "http" (default "stdio")
      --validate-tokens           With --transport http, verify each bearer token against the API before serving it
```

### Options inherited from parent commands

```
      --no-color   Disable color output
      --no-input   Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
