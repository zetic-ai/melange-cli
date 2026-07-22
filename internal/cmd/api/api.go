// Package api implements `melange api`, the authenticated raw-request escape
// hatch: any /v1 endpoint is callable through the standard transport chain
// even when no dedicated command wraps it yet.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

type options struct {
	method    string
	rawFields []string
	fields    []string
	headers   []string
	input     string
	include   bool
	silent    bool
	jq        string
	template  string
}

// NewCmdAPI builds the `melange api` command.
func NewCmdAPI(f *cmdutil.Factory) *cobra.Command {
	var opts options

	cmd := &cobra.Command{
		Use:   "api <path>",
		Short: "Make an authenticated Melange API request",
		Long: `Make an authenticated HTTP request to the Melange API and print the
raw response body to stdout.

This is the escape hatch: call any /v1 endpoint even when no dedicated
melange command wraps it yet. Requests ride the same transport chain as
every other command — stored credentials for the configured host,
automatic retries for idempotent requests (GET/HEAD, or any request
carrying an Idempotency-Key header), and standard error-envelope
handling.

The path must be relative to the configured host ("/v1/me" and "v1/me"
are equivalent; a query string like "/v1/repos?limit=5" is allowed).
Absolute URLs are rejected: credentials are bound to the configured
host and are never sent anywhere else. Run "melange auth status" to see
that host; set MELANGE_HOST to target a different one.

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
-q/--jq or -t/--template to post-process JSON. Non-2xx bodies also pass through
to stdout unfiltered while a one-line summary goes to stderr. Pagination,
polling, and Idempotency-Key generation are the caller's responsibility.

Exit codes: 0 success, 1 HTTP or transport error, 2 usage error,
4 not authenticated (or the server rejected the token), 130 interrupted.`,
		Example: `  # Agent pattern: extract a single value
  melange api /v1/me --jq .account.name

  # Create a resource with typed fields (method switches to POST)
  melange api /v1/repos -F name=whisper-tiny -F is_private=true

  # Pipe a raw JSON body from stdin
  echo '{"name":"whisper-tiny"}' | melange api /v1/repos --input -

  # Retry-safe POST via an idempotency key
  melange api /v1/models -F name=demo -H 'Idempotency-Key: 0698c9b1'

  # GET with query parameters, showing status line and headers
  melange api -X GET /v1/repos -f search=whisper --include`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAPI(f, cmd, &opts, args[0])
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&opts.method, "method", "X", "GET", "The HTTP `method` for the request")
	fl.StringArrayVarP(&opts.rawFields, "raw-field", "f", nil, "Add a string parameter in `key=value` format")
	fl.StringArrayVarP(&opts.fields, "field", "F", nil, "Add a typed parameter in `key=value` format")
	fl.StringArrayVarP(&opts.headers, "header", "H", nil, "Add a request header in `'Name: value'` format")
	fl.StringVar(&opts.input, "input", "", "The `file` to use as the raw request body (use \"-\" for stdin)")
	fl.BoolVarP(&opts.include, "include", "i", false, "Include the HTTP status line and headers in the output")
	fl.BoolVar(&opts.silent, "silent", false, "Do not print the response body")
	fl.StringVarP(&opts.jq, "jq", "q", "", "Filter the response body using a jq `expression`")
	fl.StringVarP(&opts.template, "template", "t", "", "Format the response body using a Go template")

	return cmd
}

func runAPI(f *cmdutil.Factory, cmd *cobra.Command, opts *options, pathArg string) error {
	ios := f.IOStreams

	if hasURLScheme(pathArg) {
		return cmdutil.FlagError{Err: fmt.Errorf(
			"invalid path %q: melange api only calls the configured host — pass a relative path like /v1/me (run `melange auth status` to see the host; set MELANGE_HOST to target another)", pathArg)}
	}
	if pathArg == "" {
		return cmdutil.FlagError{Err: errors.New("a path is required, e.g. /v1/me")}
	}
	path := pathArg
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	if opts.input != "" && len(opts.rawFields)+len(opts.fields) > 0 {
		return cmdutil.FlagError{Err: errors.New("cannot use --input together with -f/--raw-field or -F/--field")}
	}
	if opts.jq != "" && opts.template != "" {
		return cmdutil.FlagError{Err: errors.New("cannot use --jq and --template together")}
	}
	var exporter *cmdutil.Exporter
	if opts.jq != "" || opts.template != "" {
		e, err := cmdutil.NewExporter(opts.jq, opts.template)
		if err != nil {
			return cmdutil.FlagError{Err: err}
		}
		exporter = e
	}

	headers, err := parseHeaders(opts.headers)
	if err != nil {
		return cmdutil.FlagError{Err: err}
	}
	params, err := parseFields(opts.rawFields, opts.fields, ios.In)
	if err != nil {
		return cmdutil.FlagError{Err: err}
	}

	method := strings.ToUpper(opts.method)
	if !cmd.Flags().Changed("method") && (params != nil || opts.input != "") {
		method = http.MethodPost
	}

	var body io.Reader
	switch {
	case params != nil && method == http.MethodGet:
		if path, err = appendQuery(path, params); err != nil {
			return cmdutil.FlagError{Err: err}
		}
	case params != nil:
		data, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		body = bytes.NewReader(data)
		setDefaultHeader(headers, "Content-Type", "application/json")
	case opts.input != "":
		data, err := readInput(opts.input, ios.In)
		if err != nil {
			return cmdutil.FlagError{Err: err}
		}
		body = bytes.NewReader(data)
	}
	setDefaultHeader(headers, "Accept", "application/json")

	client, err := f.ApiClient()
	if err != nil {
		return err
	}
	resp, err := client.Do(cmd.Context(), method, path, body, headers)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	return printResponse(ios, resp, exporter, opts.include, opts.silent)
}

// printResponse implements the output contract: bodies go to stdout verbatim
// (2xx and non-2xx alike), summaries go to stderr, and the exit code follows
// the response status.
func printResponse(ios *iostreams.IOStreams, resp *http.Response, exporter *cmdutil.Exporter, include, silent bool) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if silent && !include {
			_, err := io.Copy(io.Discard, resp.Body)
			return err
		}
		return writeSuccessfulResponse(ios, resp, exporter, include, silent)
	}

	// Non-2xx: pass the body through raw (filters only apply to successful
	// responses so error diagnostics are never mangled), then summarize.
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if include {
		if err := writeHead(ios.Out, resp); err != nil {
			return err
		}
	}
	if !silent {
		if _, err := ios.Out.Write(data); err != nil {
			return err
		}
	}

	summary := fmt.Sprintf("HTTP %d", resp.StatusCode)
	var apiErr *api.Error
	if errors.As(api.ErrorFrom(resp.StatusCode, resp.Header, data), &apiErr) {
		if apiErr.Message != "" {
			summary += ": " + apiErr.Message
		}
		if apiErr.RequestID != "" {
			summary += fmt.Sprintf(" (%s)", apiErr.RequestID)
		}
	}
	fmt.Fprintf(ios.ErrOut, "melange: %s\n", summary)

	// The summary is already printed, so wrap ErrSilent; AuthError still
	// carries the exit-4 contract for rejected tokens.
	if apiErr != nil && apiErr.Type == "authentication_error" {
		return cmdutil.AuthError{Err: cmdutil.ErrSilent}
	}
	return cmdutil.ErrSilent
}

// writeSuccessfulResponse stages stdout in a private temporary file and only
// commits it after the response body has been read successfully. A connection
// reset can therefore never leave agents with a syntactically plausible but
// incomplete success payload.
func writeSuccessfulResponse(ios *iostreams.IOStreams, resp *http.Response, exporter *cmdutil.Exporter, include, silent bool) error {
	stage, err := os.CreateTemp("", "melange-api-response-*")
	if err != nil {
		return fmt.Errorf("creating response staging file: %w", err)
	}
	stageName := stage.Name()
	defer func() {
		_ = stage.Close()
		_ = os.Remove(stageName)
	}()

	stagedStreams := *ios
	stagedStreams.Out = stage
	if include {
		if err := writeHead(stage, resp); err != nil {
			return err
		}
	}

	switch {
	case silent:
		_, err = io.Copy(io.Discard, resp.Body)
	case exporter != nil:
		var data []byte
		data, err = io.ReadAll(resp.Body)
		if err == nil && len(data) > 0 {
			err = exporter.Write(&stagedStreams, json.RawMessage(data))
		}
	default:
		_, err = io.Copy(stage, resp.Body)
	}
	if err != nil {
		return err
	}
	if _, err := stage.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewinding response staging file: %w", err)
	}
	if _, err := io.Copy(ios.Out, stage); err != nil {
		return fmt.Errorf("writing response: %w", err)
	}
	return nil
}

// writeHead prints the status line and response headers (sorted, like
// curl -i) followed by a blank line.
func writeHead(w io.Writer, resp *http.Response) error {
	proto := resp.Proto
	if proto == "" {
		proto = "HTTP/1.1"
	}
	if _, err := fmt.Fprintf(w, "%s %d %s\n", proto, resp.StatusCode, http.StatusText(resp.StatusCode)); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(resp.Header)) {
		for _, value := range resp.Header[name] {
			if _, err := fmt.Fprintf(w, "%s: %s\n", name, value); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

// hasURLScheme reports whether s starts with a URL scheme followed by "://"
// (RFC 3986: ALPHA *(ALPHA / DIGIT / "+" / "-" / ".")), i.e. is an absolute
// URL. A "://" later in the string — say, in a URL-valued query parameter
// like "/v1/hooks?callback=https://x" — does not count.
func hasURLScheme(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z':
		case i > 0 && ('0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return i > 0 && strings.HasPrefix(s[i:], "://")
		}
	}
	return false
}

// readInput loads the raw request body from a file, or from stdin for "-".
func readInput(input string, stdin io.Reader) ([]byte, error) {
	if input == "-" {
		return io.ReadAll(stdin)
	}
	data, err := os.ReadFile(input)
	if err != nil {
		return nil, fmt.Errorf("reading --input: %w", err)
	}
	return data, nil
}

// setDefaultHeader sets key to value unless the user already supplied it.
func setDefaultHeader(headers map[string]string, key, value string) {
	if _, ok := headers[http.CanonicalHeaderKey(key)]; !ok {
		headers[http.CanonicalHeaderKey(key)] = value
	}
}
