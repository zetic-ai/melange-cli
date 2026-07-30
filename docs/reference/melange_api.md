## melange api

Make an authenticated Melange API request

### Synopsis

Make an authenticated HTTP request to the Melange API and print the
raw response body to stdout.

This is the escape hatch: call any /v1 endpoint even when no dedicated
melange command wraps it yet. Requests ride the same transport chain as
every other command — stored credentials for the configured host,
automatic retries for idempotent requests (GET/HEAD, or any request
carrying an Idempotency-Key header), and standard error-envelope
handling.

The path must be relative to the configured host and must resolve under
/v1 ("/v1/me" and "v1/me" are equivalent; a query string like
"/v1/repos?limit=5" is allowed). Absolute URLs and paths outside /v1 are
rejected, including ones that only leave /v1 after dot segments or
percent-escapes resolve: credentials are bound to the configured host
and to its public API. Run "melange auth status" to see that host; set
MELANGE_HOST to target a different one.

The default method is GET, switching to POST when fields or --input are
given. With an explicit "-X GET", fields become URL query parameters
instead of a JSON body.

Field syntax (repeatable; fields build a JSON object body and imply
Content-Type: application/json):

  -f key=value      string value, used verbatim
  -F key=value      typed value: true, false, null, and integers become
                    JSON types; @path inserts the file's contents as a
                    string (@- reads standard input)
  key[subkey]=v     one level of nesting: {"key":{"subkey":"v"}}
  key[]=v           arrays; repeat to append: {"key":["v1","v2"]}

--input FILE sends a request body as-is (no fields, no implied
Content-Type; add one with -H if needed). Use "-" to read from stdin.

A 2xx response body is committed to stdout only after the complete body is read,
so a timeout or connection reset cannot leave a partial success payload. The
read remains bounded by the ordinary request budget; set MELANGE_API_TIMEOUT to
a longer positive duration for a legitimately slow or large response. Use
-q/--jq or -t/--template to post-process JSON responses up to 16 MiB; omit the
filter or narrow the API query for larger bodies. Non-2xx bodies also pass
through to stdout unfiltered while a one-line summary goes to stderr.
Pagination, polling, and Idempotency-Key generation are the caller's
responsibility.

Exit codes: 0 success, 1 HTTP or transport error, 2 usage error,
4 not authenticated (or the server rejected the token), 130 interrupted.

```
melange api <path> [flags]
```

### Examples

```
  # Agent pattern: extract a single value
  melange api /v1/me --jq .account.name

  # Create a resource with typed fields (method switches to POST)
  melange api /v1/repos -F name=whisper-tiny -F is_private=true

  # Pipe a raw JSON body from stdin
  echo '{"name":"whisper-tiny"}' | melange api /v1/repos --input -

  # Retry-safe POST via an idempotency key
  melange api /v1/models -F name=demo -H 'Idempotency-Key: 0698c9b1'

  # GET with query parameters, showing status line and headers
  melange api -X GET /v1/repos -f search=whisper --include
```

### Options

```
  -F, --field key=value        Add a typed parameter in key=value format
  -H, --header 'Name: value'   Add a request header in 'Name: value' format
  -h, --help                   help for api
  -i, --include                Include the HTTP status line and headers in the output
      --input file             The file to use as the raw request body (use "-" for stdin)
  -q, --jq expression          Filter the response body using a jq expression
  -X, --method method          The HTTP method for the request (default "GET")
  -f, --raw-field key=value    Add a string parameter in key=value format
      --silent                 Do not print the response body
  -t, --template string        Format the response body using a Go template
```

### Options inherited from parent commands

```
      --format auto|table|tsv   Human output layout auto|table|tsv; auto means table on a terminal, tab-separated otherwise (default "auto")
      --no-color                Disable color output
      --no-input                Disable interactive prompts
```

### SEE ALSO

* [melange](melange.md)	 - melange — on-device AI model deployment & benchmarking
