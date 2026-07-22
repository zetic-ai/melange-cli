package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

type testEnv struct {
	f      *cmdutil.Factory
	reg    *httpmock.Registry
	in     *bytes.Buffer
	out    *bytes.Buffer
	errOut *bytes.Buffer
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func setup(t *testing.T) *testEnv {
	t.Helper()
	t.Setenv("MELANGE_DEBUG", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("NO_COLOR", "")

	ios, in, out, errOut := iostreams.Test()
	reg := &httpmock.Registry{}
	f := &cmdutil.Factory{
		IOStreams:     ios,
		Version:       "test",
		HTTPTransport: reg,
	}
	f.ApiClient = func() (*api.Client, error) {
		return cmdutil.NewAPIClient(f, "https://api.zetic.ai", "ztp_test")
	}
	return &testEnv{f: f, reg: reg, in: in, out: out, errOut: errOut}
}

func run(t *testing.T, e *testEnv, args ...string) error {
	t.Helper()
	cmd := root.NewCmdRoot(e.f)
	cmd.SetIn(e.in)
	cmd.SetOut(e.out)
	cmd.SetErr(e.errOut)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

// requestBody reads the replayable body of a recorded request.
func requestBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	require.NotNil(t, req.GetBody, "request must expose a replayable body")
	rc, err := req.GetBody()
	require.NoError(t, err)
	defer rc.Close() //nolint:errcheck
	raw, err := io.ReadAll(rc)
	require.NoError(t, err)
	return raw
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func requestJSON(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(requestBody(t, req), &m))
	return m
}

// ---------------------------------------------------------------------------
// passthrough + request shape
// ---------------------------------------------------------------------------

func TestAPIGetPassthroughBytesExact(t *testing.T) {
	e := setup(t)
	// Odd spacing and no trailing newline: the body must survive verbatim.
	body := "{\n  \"user\": {\"email\":\"dev@zetic.ai\"}\n}"
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, body))

	require.NoError(t, run(t, e, "api", "/v1/me"))

	assert.Equal(t, body, e.out.String(), "2xx bodies pass through byte-exact")
	assert.Empty(t, e.errOut.String())

	require.Len(t, e.reg.Requests, 1)
	req := e.reg.Requests[0]
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "Bearer ztp_test", req.Header.Get("Authorization"))
	assert.Equal(t, "application/json", req.Header.Get("Accept"))
}

func TestAPIPathWithoutLeadingSlash(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "{}"))

	require.NoError(t, run(t, e, "api", "v1/me"))
	require.Len(t, e.reg.Requests, 1)
	assert.Equal(t, "/v1/me", e.reg.Requests[0].URL.Path)
}

func TestAPIAbsoluteURLRejected(t *testing.T) {
	e := setup(t)
	err := run(t, e, "api", "https://evil.example.com/v1/me")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "configured host")
	assert.Empty(t, e.reg.Requests, "no request may be sent to an absolute URL")
}

func TestAPIUppercaseSchemeRejected(t *testing.T) {
	e := setup(t)
	err := run(t, e, "api", "HTTPS://evil.example.com/v1/me")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests, "scheme matching must be case-insensitive")
}

func TestAPIPathWithURLValuedQueryParamAccepted(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/hooks"), httpmock.StatusStringResponse(200, "{}"))

	require.NoError(t, run(t, e, "api", "/v1/hooks?callback=https://x"),
		"a URL-valued query parameter is not an absolute URL and must be accepted")
	require.Len(t, e.reg.Requests, 1)
	assert.Equal(t, "/v1/hooks", e.reg.Requests[0].URL.Path)
	assert.Equal(t, "callback=https://x", e.reg.Requests[0].URL.RawQuery)
}

// ---------------------------------------------------------------------------
// body construction
// ---------------------------------------------------------------------------

func TestAPIAutoPOSTWithTypedFields(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos"), httpmock.StatusStringResponse(201, "{}"))

	require.NoError(t, run(t, e, "api", "/v1/repos",
		"-f", "name=whisper-tiny",
		"-F", "limit=5",
		"-F", "is_private=true",
		"-F", "flag=false",
		"-F", "nothing=null",
		"-F", "version=1.2"))

	require.Len(t, e.reg.Requests, 1)
	req := e.reg.Requests[0]
	assert.Equal(t, "POST", req.Method, "fields auto-switch the method to POST")
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))

	body := requestJSON(t, req)
	assert.Equal(t, "whisper-tiny", body["name"], "-f values stay strings")
	assert.Equal(t, float64(5), body["limit"], "-F integers become JSON numbers")
	assert.Equal(t, true, body["is_private"])
	assert.Equal(t, false, body["flag"])
	assert.Contains(t, body, "nothing")
	assert.Nil(t, body["nothing"])
	assert.Equal(t, "1.2", body["version"], "non-integer numerics stay strings")
}

func TestAPINestedAndArrayFields(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos"), httpmock.StatusStringResponse(200, "{}"))

	require.NoError(t, run(t, e, "api", "/v1/repos",
		"-f", "tags[]=asr",
		"-f", "tags[]=tiny",
		"-F", "meta[count]=3",
		"-f", "meta[env]=prod"))

	body := requestJSON(t, e.reg.Requests[0])
	assert.Equal(t, []any{"asr", "tiny"}, body["tags"])
	assert.Equal(t, map[string]any{"count": float64(3), "env": "prod"}, body["meta"])
}

func TestAPIFieldFromFile(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos"), httpmock.StatusStringResponse(200, "{}"))

	dir := t.TempDir()
	path := dir + "/desc.txt"
	require.NoError(t, writeFile(path, "from a file"))

	require.NoError(t, run(t, e, "api", "/v1/repos", "-F", "description=@"+path))
	body := requestJSON(t, e.reg.Requests[0])
	assert.Equal(t, "from a file", body["description"], "@path inserts file contents as a string")
}

func TestAPIFieldFromStdin(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos"), httpmock.StatusStringResponse(200, "{}"))
	e.in.WriteString("from stdin")

	require.NoError(t, run(t, e, "api", "/v1/repos", "-F", "description=@-"))
	body := requestJSON(t, e.reg.Requests[0])
	assert.Equal(t, "from stdin", body["description"], "@- inserts stdin as a string")
}

func TestAPIRepeatedStdinFieldExits2(t *testing.T) {
	e := setup(t)
	e.in.WriteString("only once")

	err := run(t, e, "api", "/v1/repos", "-F", "a=@-", "-F", "b=@-")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "standard input already consumed by a previous @- value")
	assert.Empty(t, e.reg.Requests, "the request must not be sent")
}

func TestAPIGetFieldsBecomeQueryParams(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"), httpmock.StatusStringResponse(200, "[]"))

	require.NoError(t, run(t, e, "api", "-X", "GET", "/v1/repos",
		"-f", "search=whisper", "-F", "limit=5"))

	require.Len(t, e.reg.Requests, 1)
	req := e.reg.Requests[0]
	assert.Equal(t, "GET", req.Method)
	q := req.URL.Query()
	assert.Equal(t, "whisper", q.Get("search"))
	assert.Equal(t, "5", q.Get("limit"))
	assert.Nil(t, req.Body, "GET with fields sends no body")
}

func TestAPIQueryInPathMergesWithFields(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/repos"), httpmock.StatusStringResponse(200, "[]"))

	require.NoError(t, run(t, e, "api", "-X", "GET", "/v1/repos?limit=5", "-f", "search=whisper"))

	q := e.reg.Requests[0].URL.Query()
	assert.Equal(t, "5", q.Get("limit"), "query already in the path survives")
	assert.Equal(t, "whisper", q.Get("search"))
}

func TestAPIInputFromStdin(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos"), httpmock.StatusStringResponse(200, "{}"))

	raw := "{ \"raw\": true }\n"
	e.in.WriteString(raw)

	require.NoError(t, run(t, e, "api", "/v1/repos", "--input", "-"))

	require.Len(t, e.reg.Requests, 1)
	req := e.reg.Requests[0]
	assert.Equal(t, "POST", req.Method, "--input auto-switches the method to POST")
	assert.Equal(t, raw, string(requestBody(t, req)), "--input bodies are sent as-is")
	assert.Empty(t, req.Header.Get("Content-Type"),
		"--input sets no Content-Type unless one is passed via -H")
}

func TestAPIInputWithContentTypeHeader(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("POST", "/v1/repos"), httpmock.StatusStringResponse(200, "{}"))
	e.in.WriteString("name,type\na,b\n")

	require.NoError(t, run(t, e, "api", "/v1/repos", "--input", "-", "-H", "Content-Type: text/csv"))
	assert.Equal(t, "text/csv", e.reg.Requests[0].Header.Get("Content-Type"))
}

func TestAPIInputConflictsWithFields(t *testing.T) {
	e := setup(t)
	err := run(t, e, "api", "/v1/repos", "--input", "-", "-f", "name=x")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestAPIInvalidFieldSyntaxExits2(t *testing.T) {
	e := setup(t)
	for _, spec := range []string{"noequals", "a[b][c]=deep", "[]=nobase"} {
		err := run(t, e, "api", "/v1/repos", "-f", spec)
		require.Error(t, err, "spec %q", spec)
		assert.Equal(t, 2, cmdutil.ExitCode(err), "spec %q", spec)
	}
	assert.Empty(t, e.reg.Requests)
}

// ---------------------------------------------------------------------------
// headers
// ---------------------------------------------------------------------------

func TestAPICustomHeaderAndIdempotencyRetry(t *testing.T) {
	e := setup(t)
	// A POST with an Idempotency-Key must ride the transport's retry policy.
	e.reg.Register(httpmock.REST("POST", "/v1/models"), httpmock.StatusStringResponse(502, "bad gateway"))
	e.reg.Register(httpmock.REST("POST", "/v1/models"), httpmock.StatusStringResponse(200, `{"ok":true}`))

	require.NoError(t, run(t, e, "api", "/v1/models",
		"-F", "name=demo", "-H", "Idempotency-Key: idem-42"))

	require.Len(t, e.reg.Requests, 2, "Idempotency-Key makes the POST retry-eligible")
	assert.Equal(t, "idem-42", e.reg.Requests[0].Header.Get("Idempotency-Key"))
	assert.Equal(t, `{"ok":true}`, e.out.String())
	e.reg.Verify(t)
}

func TestAPIAuthorizationOverrideRejected(t *testing.T) {
	e := setup(t)
	for _, h := range []string{"Authorization: Bearer stolen", "authorization:Basic abc"} {
		err := run(t, e, "api", "/v1/me", "-H", h)
		require.Error(t, err, "header %q", h)
		assert.Equal(t, 2, cmdutil.ExitCode(err), "header %q", h)
		assert.Contains(t, err.Error(), "cannot override Authorization")
	}
	assert.Empty(t, e.reg.Requests, "no request may carry a user-supplied Authorization")
}

func TestAPIInvalidHeaderSyntaxExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "api", "/v1/me", "-H", "no-colon-here")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

// ---------------------------------------------------------------------------
// responses
// ---------------------------------------------------------------------------

func TestAPINon2xxEnvelopePassthroughAndSummary(t *testing.T) {
	e := setup(t)
	body := `{"type":"error","error":{"type":"not_found_error","message":"repository zetic/nope not found"},"request_id":"req_4"}`
	e.reg.Register(httpmock.REST("GET", "/v1/repos/zetic/nope"), httpmock.StatusStringResponse(404, body))

	err := run(t, e, "api", "/v1/repos/zetic/nope")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.ErrorIs(t, err, cmdutil.ErrSilent, "the command prints its own summary")

	assert.Equal(t, body, e.out.String(), "error bodies still pass through to stdout")
	assert.Equal(t, "melange: HTTP 404: repository zetic/nope not found (req_4)\n", e.errOut.String())
}

func TestAPINon2xxWithoutEnvelope(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/down"), httpmock.StatusStringResponse(500, "upstream exploded"))

	err := run(t, e, "api", "/v1/down")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Equal(t, "upstream exploded", e.out.String())
	assert.Equal(t, "melange: HTTP 500: upstream exploded\n", e.errOut.String())
}

func TestAPI401AuthenticationErrorExits4(t *testing.T) {
	e := setup(t)
	body := `{"type":"error","error":{"type":"authentication_error","message":"invalid token"},"request_id":"req_9"}`
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(401, body))

	err := run(t, e, "api", "/v1/me")
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))
	assert.ErrorIs(t, err, cmdutil.ErrSilent, "the summary is already printed; the runner must stay quiet")
	assert.Equal(t, body, e.out.String())
	assert.Equal(t, "melange: HTTP 401: invalid token (req_9)\n", e.errOut.String())
}

func TestAPINoTokenExits4(t *testing.T) {
	e := setup(t)
	e.f.ApiClient = func() (*api.Client, error) {
		return nil, cmdutil.AuthError{Err: errors.New("not logged in to api.zetic.ai; run `melange auth login` or set MELANGE_API_KEY")}
	}

	err := run(t, e, "api", "/v1/me")
	require.Error(t, err)
	assert.Equal(t, 4, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "melange auth login")
	assert.Empty(t, e.reg.Requests)
}

func TestAPIJQFilter(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"),
		httpmock.StatusStringResponse(200, `{"account":{"name":"zetic","type":"org"}}`))

	require.NoError(t, run(t, e, "api", "/v1/me", "--jq", ".account.name"))
	assert.Equal(t, "zetic\n", e.out.String())
}

func TestAPITemplate(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"),
		httpmock.StatusStringResponse(200, `{"account":{"name":"zetic","type":"org"}}`))

	require.NoError(t, run(t, e, "api", "/v1/me", "-t", "{{.account.type}}"))
	assert.Equal(t, "org", e.out.String())
}

func TestAPIJQTemplateConflictExits2(t *testing.T) {
	e := setup(t)
	err := run(t, e, "api", "/v1/me", "-q", ".a", "-t", "{{.a}}")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Empty(t, e.reg.Requests)
}

func TestAPIInclude(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"),
		httpmock.WithHeader(httpmock.StatusStringResponse(200, `{"a":1}`), "X-Request-Id", "req_1"))

	require.NoError(t, run(t, e, "api", "/v1/me", "--include"))

	want := "HTTP/1.1 200 OK\n" +
		"X-Request-Id: req_1\n" +
		"\n" +
		`{"a":1}`
	assert.Equal(t, want, e.out.String())
}

func TestAPISilentDiscardsBody(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, `{"a":1}`))

	require.NoError(t, run(t, e, "api", "/v1/me", "--silent"))
	assert.Empty(t, e.out.String())
	assert.Empty(t, e.errOut.String())
}

func TestAPISilentNon2xxStillFails(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/nope"), httpmock.StatusStringResponse(404, "missing"))

	err := run(t, e, "api", "/v1/nope", "--silent")
	require.Error(t, err)
	assert.Equal(t, 1, cmdutil.ExitCode(err))
	assert.Empty(t, e.out.String(), "--silent discards the body even on errors")
	assert.Equal(t, "melange: HTTP 404: missing\n", e.errOut.String())
}

func TestAPIPartialSuccessfulBodyNeverLeaksToStdout(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(fmt.Sprintf("include=%t", include), func(t *testing.T) {
			e := setup(t)
			e.reg.Register(httpmock.REST("GET", "/v1/partial"), func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Proto:      "HTTP/1.1",
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(io.MultiReader(
						bytes.NewBufferString(`{"partial":`),
						failingReader{err: io.ErrUnexpectedEOF},
					)),
					Request: req,
				}, nil
			})

			args := []string{"api", "/v1/partial"}
			if include {
				args = append(args, "--include")
			}
			err := run(t, e, args...)
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			assert.Empty(t, e.out.String(), "failed response reads must leave the data stream transactional")
		})
	}
}

// ---------------------------------------------------------------------------
// credential safety
// ---------------------------------------------------------------------------

func TestAPICrossHostRedirectDoesNotLeakAuthorization(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/artifacts/download"),
		httpmock.WithHeader(httpmock.StatusStringResponse(302, ""),
			"Location", "https://storage.example.com/blob"))
	e.reg.Register(httpmock.REST("GET", "/blob"), httpmock.StatusStringResponse(200, "BLOB"))

	require.NoError(t, run(t, e, "api", "/v1/artifacts/download"))
	assert.Equal(t, "BLOB", e.out.String())

	require.Len(t, e.reg.Requests, 2)
	assert.Equal(t, "Bearer ztp_test", e.reg.Requests[0].Header.Get("Authorization"))
	assert.Equal(t, "storage.example.com", e.reg.Requests[1].URL.Host)
	assert.Empty(t, e.reg.Requests[1].Header.Get("Authorization"),
		"the token must never follow a redirect to a foreign host")
}

// ---------------------------------------------------------------------------
// help
// ---------------------------------------------------------------------------

func TestAPIHelpDocumentsContract(t *testing.T) {
	e := setup(t)
	require.NoError(t, run(t, e, "api", "--help"))
	help := e.out.String()
	assert.Contains(t, help, "escape hatch")
	assert.Contains(t, help, "key[subkey]=")
	assert.Contains(t, help, "Idempotency-Key")
	assert.Contains(t, help, "Exit codes")
	assert.Contains(t, help, "melange api /v1/me --jq .account.name")
	assert.Contains(t, help, "--input -")
	assert.Contains(t, help, "committed to stdout only after the complete body is read")
	assert.Contains(t, help, "MELANGE_API_TIMEOUT")
}
