package mcp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

const downloadPath = "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1/targets/tgt_abc/download-authorizations"

// The authorization fixtures are written with sorted keys because redaction
// decodes and re-emits the body — the same documented deviation from
// byte-exact output `melange model download --json` makes. Everything except
// the artifact url must therefore come back byte-identical, including the `<`
// and `&` that an HTML-escaping re-marshal would rewrite as &lt; and &amp;.
const (
	authorizationBody = `{"artifacts":[{"checksum":"sha256:ab12","name":"whisper <int8> & cpu.zmc",` +
		`"size":5368709120,"url":"https://storage.example/whisper.zmc?sig=abc&exp=1"}],` +
		`"authorization_id":"dla_1","expires_at":"2026-01-01T00:00:00Z"}`
	redactedAuthorizationBody = `{"artifacts":[{"checksum":"sha256:ab12","name":"whisper <int8> & cpu.zmc",` +
		`"size":5368709120,"url":"<redacted>"}],` +
		`"authorization_id":"dla_1","expires_at":"2026-01-01T00:00:00Z"}`
)

// confirmedDownloadArgs are the arguments of an authorized request; tests that
// vary one aspect copy them.
func confirmedDownloadArgs() map[string]any {
	return map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
		"target_id": "tgt_abc", "confirm": true,
	}
}

func TestRequestModelDownloadWithoutConfirmationNeverCallsTheAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"confirm absent", map[string]any{
			"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "target_id": "tgt_abc",
		}},
		{"confirm false", map[string]any{
			"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
			"target_id": "tgt_abc", "confirm": false,
		}},
		{"confirm false with include_urls", map[string]any{
			"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
			"target_id": "tgt_abc", "confirm": false, "include_urls": true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "request_model_download", tc.args)

			assert.True(t, res.IsError)
			text := textOf(t, res)
			// Discriminating on the refusal's own words: an IsError result with
			// an empty registry could equally be a schema failure or an
			// unmatched stub, neither of which proves the gate ran.
			assert.Contains(t, text, "nothing was charged")
			assert.Contains(t, text, "explicit consent from the user",
				"the agent is told to get consent, not just to retry with confirm")
			assert.Contains(t, text, "confirm: true")
			assert.Empty(t, reg.Requests, "an unconfirmed billable request never reaches the API")
		})
	}
}

func TestRequestModelDownloadInvalidRepoArgumentIsToolErrorWithoutAnAPICall(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	args := confirmedDownloadArgs()
	args["repo"] = "whisper-tiny"
	res := callTool(t, cs, "request_model_download", args)

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "ACCOUNT/NAME")
	assert.Empty(t, reg.Requests, "a malformed repo is never charged against a guessed account")
}

func TestRequestModelDownloadRedactsSignedURLsByDefault(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", downloadPath), jsonBody(http.StatusCreated, authorizationBody))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "request_model_download", confirmedDownloadArgs())

	assert.False(t, res.IsError)
	assert.Equal(t, redactedAuthorizationBody, textOf(t, res),
		"only the signed url changes; every other byte — checksum, name, size, id, expiry — survives")

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, redactedAuthorizationBody)
	assert.NotContains(t, wire.String(), "sig=abc",
		"a short-lived credential must not reach the transcript by any route")
	reg.Verify(t)
}

func TestRequestModelDownloadRedactionPreservesNumericLiterals(t *testing.T) {
	// 2^53+1 cannot survive a float64 round trip: decoding the body through
	// json.Number is what keeps an artifact size exactly as the API sent it.
	body := `{"artifacts":[{"name":"big.zmc","size":9007199254740993,` +
		`"url":"https://storage.example/big.zmc?sig=xyz"}],"authorization_id":"dla_2",` +
		`"expires_at":"2026-01-01T00:00:00Z"}`
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", downloadPath), jsonBody(http.StatusCreated, body))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "request_model_download", confirmedDownloadArgs())

	assert.False(t, res.IsError)
	assert.Equal(t,
		`{"artifacts":[{"name":"big.zmc","size":9007199254740993,"url":"<redacted>"}],`+
			`"authorization_id":"dla_2","expires_at":"2026-01-01T00:00:00Z"}`,
		textOf(t, res))
	reg.Verify(t)
}

func TestRequestModelDownloadIncludeURLsPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", downloadPath), jsonBody(http.StatusCreated, authorizationBody))

	cs, wire := connect(t, registryProvider(t, reg))
	args := confirmedDownloadArgs()
	args["include_urls"] = true
	res := callTool(t, cs, "request_model_download", args)

	assert.False(t, res.IsError)
	assert.Equal(t, authorizationBody, textOf(t, res),
		"an explicit include_urls returns the authorization bytes untouched")

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, authorizationBody)
	reg.Verify(t)
}

func TestRequestModelDownloadUnparsableResponseNeverLeaksTheRawBody(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", downloadPath),
		jsonBody(http.StatusCreated, `["https://storage.example/whisper.zmc?sig=abc"]`))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "request_model_download", confirmedDownloadArgs())

	assert.True(t, res.IsError, "an authorization that cannot be understood is never returned as-is")
	assert.NotContains(t, textOf(t, res), "sig=abc",
		"echoing an unparsable body back would leak the credential redaction exists to hide")
	reg.Verify(t)
}

func TestRedactAuthorizationFailsClosed(t *testing.T) {
	// The handler must never fall back to the raw body when redaction fails,
	// so redaction itself must return nothing at all rather than a partially
	// redacted document — and its error must not quote the body it rejected.
	out, err := redactAuthorization([]byte(`["https://storage.example/whisper.zmc?sig=abc"]`))

	require.Error(t, err)
	assert.Nil(t, out)
	assert.NotContains(t, err.Error(), "sig=abc")
}

func TestRequestModelDownloadCarriesAFreshIdempotencyKeyPerCall(t *testing.T) {
	reg := &httpmock.Registry{}
	for range 2 {
		reg.Register(httpmock.REST("POST", downloadPath), jsonBody(http.StatusCreated, authorizationBody))
	}

	cs, _ := connect(t, registryProvider(t, reg))
	for range 2 {
		assert.False(t, callTool(t, cs, "request_model_download", confirmedDownloadArgs()).IsError)
	}

	require.Len(t, reg.Requests, 2)
	first := reg.Requests[0].Header.Get("Idempotency-Key")
	second := reg.Requests[1].Header.Get("Idempotency-Key")
	// The key is what lets the transport replay one authorization after a 5xx
	// without charging twice; sharing it across calls would instead make a
	// second, separately consented request replay the first one's URLs.
	assert.NotEmpty(t, first, "a billable POST must be replay-safe within its own call")
	assert.NotEqual(t, first, second, "each confirmed call is its own authorization")
	reg.Verify(t)
}

func TestRequestModelDownloadQuotaFailureIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", downloadPath), httpmock.WithHeader(
		httpmock.JSONResponse(http.StatusTooManyRequests, json.RawMessage(
			`{"type":"error","error":{"type":"rate_limit_error","message":"bandwidth quota exhausted"},"request_id":"req_41"}`)),
		"Retry-After", "60"))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "request_model_download", confirmedDownloadArgs())

	assert.True(t, res.IsError)
	text := textOf(t, res)
	assert.Contains(t, text, "bandwidth quota exhausted")
	assert.Contains(t, text, "Retry after 60 seconds")
	reg.Verify(t)
}

func TestRequestModelDownloadAnnotationsAndDescription(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	assertMutatingAnnotations(t, cs, "request_model_download", false, false)

	tool := toolNamed(t, cs, "request_model_download")
	// The description is where an agent learns the cost, the consent gate, the
	// redaction default, and that a local user is better served by the CLI.
	assert.Contains(t, tool.Description, "BILLABLE")
	assert.Contains(t, tool.Description, "explicit consent")
	// The tool is stateless: a retry after an ambiguous failure authorizes —
	// and charges — a second time, on a consent the user gave once. The
	// description is the only place an agent can learn that before retrying.
	assert.Contains(t, tool.Description, "Every confirmed call is charged separately")
	assert.Contains(t, tool.Description, "check with the user before retrying")
	assert.Contains(t, tool.Description, `"<redacted>"`)
	assert.Contains(t, tool.Description, "melange model download")

	schema, err := json.Marshal(tool.InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"required":["repo","model_key","target_id"]`,
		"confirm stays optional in the schema so the handler's consent guidance is what an agent sees")
	assert.Contains(t, string(schema), "include_urls")
}
