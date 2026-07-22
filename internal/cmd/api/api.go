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

const (
	// Keep ordinary API responses independent of the filesystem while bounding
	// memory used by the transactional stdout contract. Larger bodies spill to
	// a private temporary file before anything is committed to stdout.
	responseMemoryLimit = 1 << 20
	// Error bodies remain raw streams on stdout. Only this prefix is retained
	// to derive the one-line stderr summary.
	errorSummaryLimit     = 32 << 10
	filteredResponseLimit = 16 << 20
)

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
-q/--jq or -t/--template to post-process JSON responses up to 16 MiB; omit the
filter or narrow the API query for larger bodies. Non-2xx bodies also pass
through to stdout unfiltered while a one-line summary goes to stderr.
Pagination, polling, and Idempotency-Key generation are the caller's
responsibility.

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
	// responses so error diagnostics are never mangled). Retain only a bounded
	// prefix for the summary; arbitrarily large error bodies must not be loaded
	// into memory merely to preserve their stdout contract.
	if include {
		if err := writeHead(ios.Out, resp); err != nil {
			return err
		}
	}
	prefix := &prefixWriter{remaining: errorSummaryLimit}
	destination := io.Writer(io.Discard)
	if !silent {
		destination = ios.Out
	}
	if _, err := io.Copy(destination, io.TeeReader(resp.Body, prefix)); err != nil {
		return err
	}
	data := prefix.Bytes()

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

// writeSuccessfulResponse stages stdout in bounded memory, spilling only large
// responses to a private temporary file, and commits it after the complete
// body has been read. A connection reset or spill failure can therefore never
// leave agents with a syntactically plausible but incomplete success payload.
func writeSuccessfulResponse(ios *iostreams.IOStreams, resp *http.Response, exporter *cmdutil.Exporter, include, silent bool) error {
	stage := newResponseStage(responseMemoryLimit)
	defer stage.Close()
	var err error

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
		bodyStage := newResponseStage(responseMemoryLimit)
		defer bodyStage.Close()
		if _, err = io.Copy(bodyStage, resp.Body); err == nil {
			if bodyStage.Size() > filteredResponseLimit {
				err = fmt.Errorf(
					"filtered response exceeds the 16 MiB processing limit; omit --jq/--template or narrow the API query")
			} else {
				var data []byte
				data, err = bodyStage.ReadAll()
				if err == nil && len(data) > 0 {
					err = exporter.Write(&stagedStreams, json.RawMessage(data))
				}
			}
		}
	default:
		_, err = io.Copy(stage, resp.Body)
	}
	if err != nil {
		return err
	}
	if err := stage.CopyTo(ios.Out); err != nil {
		return fmt.Errorf("writing response: %w", err)
	}
	return nil
}

// responseStage is an append-only transactional buffer. It retains at most
// memoryLimit bytes in RAM and migrates atomically to a private temp file when
// the next write would exceed the limit.
type responseStage struct {
	memoryLimit int
	memory      bytes.Buffer
	file        *os.File
	size        int64
}

func newResponseStage(memoryLimit int) *responseStage {
	return &responseStage{memoryLimit: memoryLimit}
}

func (s *responseStage) Write(p []byte) (int, error) {
	if s.file != nil {
		n, err := s.file.Write(p)
		s.size += int64(n)
		return n, err
	}
	if s.memory.Len()+len(p) <= s.memoryLimit {
		n, err := s.memory.Write(p)
		s.size += int64(n)
		return n, err
	}

	f, err := os.CreateTemp("", "melange-api-response-*")
	if err != nil {
		return 0, fmt.Errorf("staging response in temporary file: %w", err)
	}
	if _, err := f.Write(s.memory.Bytes()); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return 0, fmt.Errorf("staging response in temporary file: %w", err)
	}
	s.file = f
	s.memory.Reset()
	n, err := s.file.Write(p)
	s.size += int64(n)
	return n, err
}

func (s *responseStage) Size() int64 { return s.size }

func (s *responseStage) reader() (io.Reader, error) {
	if s.file == nil {
		return bytes.NewReader(s.memory.Bytes()), nil
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewinding response staging file: %w", err)
	}
	return s.file, nil
}

func (s *responseStage) CopyTo(w io.Writer) error {
	r, err := s.reader()
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return err
}

func (s *responseStage) ReadAll() ([]byte, error) {
	r, err := s.reader()
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func (s *responseStage) Close() {
	if s.file == nil {
		return
	}
	name := s.file.Name()
	_ = s.file.Close()
	_ = os.Remove(name)
}

// prefixWriter accepts the complete stream while retaining only its first
// remaining bytes.
type prefixWriter struct {
	bytes.Buffer
	remaining int
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.remaining > 0 {
		keep := min(w.remaining, len(p))
		_, _ = w.Buffer.Write(p[:keep])
		w.remaining -= keep
	}
	return n, nil
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
