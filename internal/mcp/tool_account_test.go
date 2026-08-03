package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// meBody uses non-alphabetical key order so any re-marshal (which would sort
// map keys) breaks byte-equality assertions.
const meBody = `{"user":{"email":"dev@zetic.ai","nickname":"dev"},` +
	`"account":{"name":"zetic","type":"org"},` +
	`"token":{"name":"ci-token","scopes":["read","write"]}}`

// syncWriter guards the wire log: transport reads are concurrent to writes.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// jsonBody stubs a JSON response whose body is exactly the given bytes.
//
// httpmock.JSONResponse cannot serve a fixture containing `<`, `>`, or `&`:
// it marshals through encoding/json, which rewrites them as <, >,
// and & — so a body written to prove those characters survive would
// arrive at the client already escaped, and the property would be
// unobservable. This responder writes the fixture verbatim, exactly as a real
// API would.
func jsonBody(status int, body string) httpmock.Responder {
	return httpmock.WithHeader(
		httpmock.StatusStringResponse(status, body), "Content-Type", "application/json")
}

// assertStructuredContentOnWire asserts that the tool result framed on the
// wire carried want as its structuredContent.
//
// want is compared after json.Marshal because the SDK's JSON-RPC framing
// HTML-escapes when it writes the frame. That escaping is lossless — the
// client decodes & back to `&`, which is why textOf sees the original
// characters — and it is the transport's business, not ours. What this pins
// is that the handler's bytes are the bytes framed.
func assertStructuredContentOnWire(t *testing.T, wire *syncWriter, want string) {
	t.Helper()
	framed, err := json.Marshal(json.RawMessage(want))
	require.NoError(t, err)
	assert.Contains(t, wire.String(), `"structuredContent":`+string(framed),
		"the response bytes cross the wire verbatim")
}

// registryProvider returns a ClientProvider whose generated client talks to
// the httpmock registry.
func registryProvider(t *testing.T, reg *httpmock.Registry) ClientProvider {
	t.Helper()
	return NewStaticProvider(func() (*gen.ClientWithResponses, error) {
		return gen.NewClientWithResponses("https://api.zetic.ai",
			gen.WithHTTPClient(&http.Client{Transport: reg}))
	})
}

// connect wires a fresh server for provider to an in-memory client session
// and returns the session plus the client-side wire log.
func connect(t *testing.T, provider ClientProvider) (*mcp.ClientSession, *syncWriter) {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	srv := New(Deps{Clients: provider, Version: "test"}, Options{})
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Wait() })

	wire := &syncWriter{}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx,
		&mcp.LoggingTransport{Transport: clientTransport, Writer: wire}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession, wire
}

func TestWhoamiRoundTripPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"),
		httpmock.JSONResponse(200, json.RawMessage(meBody)))

	cs, wire := connect(t, registryProvider(t, reg))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	require.NoError(t, err)

	assert.False(t, res.IsError)
	assertConformsToOutputSchema(t, "whoami", res)
	assert.Equal(t, meBody, textOf(t, res), "text mirror carries the exact body")

	structured, merr := json.Marshal(res.StructuredContent)
	require.NoError(t, merr)
	assert.JSONEq(t, meBody, string(structured))

	// Byte-exactness across the wire: the raw response frame must embed the
	// stubbed body verbatim (key order intact — no typed re-marshal).
	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+meBody)

	reg.Verify(t)
}

func TestWhoamiAPIAuthErrorIsToolErrorWithRemediation(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"),
		httpmock.JSONResponse(401, json.RawMessage(
			`{"type":"error","error":{"type":"authentication_error","message":"invalid token"},"request_id":"req_1"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	require.NoError(t, err, "API failures are tool errors, not protocol errors")

	assert.True(t, res.IsError)
	text := textOf(t, res)
	assert.Contains(t, text, "invalid token")
	assert.Contains(t, text, "melange auth login")
	assert.Contains(t, text, "MELANGE_API_KEY")
}

func TestWhoamiNoTokenResolvedIsToolErrorWithRemediation(t *testing.T) {
	provider := NewStaticProvider(func() (*gen.ClientWithResponses, error) {
		return nil, cmdutil.AuthError{Err: errors.New("not logged in to api.zetic.ai")}
	})

	cs, _ := connect(t, provider)
	// Twice: the cached resolve failure must surface on every call.
	for range 2 {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
		require.NoError(t, err)
		assert.True(t, res.IsError)
		text := textOf(t, res)
		assert.Contains(t, text, "not logged in to api.zetic.ai")
		assert.Contains(t, text, "melange auth login")
		assert.Contains(t, text, "MELANGE_API_KEY")
	}
}

// Section bodies for get_account_info, again in non-alphabetical key order.
//
// The `<` and `&` characters are equally deliberate: plan labels are prose and
// carry them, and json.Marshal rewrites them as < and & whenever it
// re-emits a json.RawMessage. An envelope built with HTML escaping fails on
// these bodies.
const (
	usageBody  = `{"prompts":120,"model_uploads":3,"active_devices":7,"bandwidth":204800}`
	quotasBody = `{"prompts":{"used":120,"limit":1000,"remaining":880},` +
		`"model_uploads":{"used":3,"limit":null,"remaining":null},` +
		`"bandwidth":{"used":204800,"limit":10737418240,"remaining":10737213440},` +
		`"active_devices":{"used":7,"limit":50,"remaining":43},"note":"spikes & bursts included"}`
	planBody = `{"plan":"pro","label":"Pro & Team <beta>","is_trial":false,"trial_ends_at":null}`
)

// stubAccountSections registers the section endpoints named in sections.
func stubAccountSections(reg *httpmock.Registry, sections ...string) {
	bodies := map[string]struct{ path, body string }{
		"usage":  {"/v1/usage", usageBody},
		"quotas": {"/v1/usage/quotas", quotasBody},
		"plan":   {"/v1/billing/plan", planBody},
	}
	for _, s := range sections {
		reg.Register(httpmock.REST("GET", bodies[s].path), jsonBody(200, bodies[s].body))
	}
}

func TestGetAccountInfoWithoutIncludeReturnsEverySection(t *testing.T) {
	reg := &httpmock.Registry{}
	stubAccountSections(reg, "usage", "quotas", "plan")

	cs, wire := connect(t, registryProvider(t, reg))
	// nil arguments marshal to a literal "arguments": null — the shape that
	// crashes the SDK's default-filling, so "all sections" is a handler
	// default, never a schema one.
	res := callTool(t, cs, "get_account_info", nil)

	assert.False(t, res.IsError)
	want := `{"usage":` + usageBody + `,"quotas":` + quotasBody + `,"plan":` + planBody + `}`
	assert.Equal(t, want, textOf(t, res),
		"the envelope names each section and keeps every response's bytes intact, "+
			"including the < and & an escaping re-marshal would rewrite")

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, want)
	reg.Verify(t)
}

func TestGetAccountInfoIncludeFetchesOnlyTheRequestedSections(t *testing.T) {
	for _, tc := range []struct {
		name     string
		include  []string
		stub     []string
		want     string
		requests int
	}{
		{
			name: "quotas alone", include: []string{"quotas"}, stub: []string{"quotas"},
			want: `{"quotas":` + quotasBody + `}`, requests: 1,
		},
		{
			name: "plan alone", include: []string{"plan"}, stub: []string{"plan"},
			want: `{"plan":` + planBody + `}`, requests: 1,
		},
		{
			// The envelope keys stay in their canonical order however the
			// caller wrote include, so the same request always reads the same.
			name: "reversed order", include: []string{"plan", "usage"}, stub: []string{"usage", "plan"},
			want: `{"usage":` + usageBody + `,"plan":` + planBody + `}`, requests: 2,
		},
		{
			name: "repeated section", include: []string{"usage", "usage"}, stub: []string{"usage"},
			want: `{"usage":` + usageBody + `}`, requests: 1,
		},
		{
			// An explicit empty list reads as "no preference", not "no data":
			// an empty envelope would answer nothing at all.
			name: "empty include", include: []string{}, stub: []string{"usage", "quotas", "plan"},
			want:     `{"usage":` + usageBody + `,"quotas":` + quotasBody + `,"plan":` + planBody + `}`,
			requests: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			stubAccountSections(reg, tc.stub...)

			cs, _ := connect(t, registryProvider(t, reg))
			res := callTool(t, cs, "get_account_info", map[string]any{"include": tc.include})

			assert.False(t, res.IsError)
			assert.Equal(t, tc.want, textOf(t, res),
				"an unrequested section is absent from the envelope, not null")
			assert.Len(t, reg.Requests, tc.requests,
				"a section nobody asked for is never fetched")
			reg.Verify(t)
		})
	}
}

func TestGetAccountInfoRejectsAnUnknownSectionBeforeCallingTheAPI(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))

	// The vocabulary must reach the client, not just the handler: an
	// unadvertised section would otherwise be dropped, and a silently smaller
	// envelope reads like a section the account does not have.
	schema, err := json.Marshal(toolNamed(t, cs, "get_account_info").InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"enum":["usage","quotas","plan"]`,
		"the section vocabulary is advertised")

	for _, tc := range []struct {
		name    string
		include []string
	}{
		{"unknown section", []string{"billing"}},
		{"one bad section among good ones", []string{"usage", "billing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "get_account_info", map[string]any{"include": tc.include})

			assert.True(t, res.IsError)
			// The SDK prefixes schema-validation failures this way; without it,
			// an IsError result could just as well be the unmatched-stub
			// transport error, which would pass with no enum at all.
			assert.Contains(t, textOf(t, res), `validating "arguments"`,
				"the section is rejected by the schema, not by a failed request")
			assert.Empty(t, reg.Requests, "no section is fetched for an invalid request")
		})
	}
}

func TestGetAccountInfoSectionFailureSurfacesInsteadOfAPartialEnvelope(t *testing.T) {
	reg := &httpmock.Registry{}
	stubAccountSections(reg, "usage")
	reg.Register(httpmock.REST("GET", "/v1/usage/quotas"),
		httpmock.JSONResponse(http.StatusForbidden, json.RawMessage(
			`{"type":"error","error":{"type":"permission_error","message":"token cannot read quotas"},"request_id":"req_15"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_account_info", map[string]any{"include": []string{"usage", "quotas"}})

	assert.True(t, res.IsError, "a half-built envelope is never returned as success")
	text := textOf(t, res)
	assert.Contains(t, text, "token cannot read quotas")
	assert.Contains(t, text, "melange auth status")
	reg.Verify(t)
}

func TestGetAccountInfoAnnotationsAndDescription(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	assertReadOnlyAnnotations(t, cs, "get_account_info")

	tool := toolNamed(t, cs, "get_account_info")
	// The description is the only place an agent learns the envelope's shape
	// and that 'remaining' — not limit minus used — is the real headroom.
	assert.Contains(t, tool.Description, `{"usage":`)
	assert.Contains(t, tool.Description, "remaining")
}

func TestWhoamiToolAnnotations(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	var whoami *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "whoami" {
			whoami = tool
		}
	}
	require.NotNil(t, whoami, "whoami must be registered")
	assert.False(t, strings.Contains(whoami.Name, "melange"), "tool names are unprefixed")
	require.NotNil(t, whoami.Annotations)
	assert.True(t, whoami.Annotations.ReadOnlyHint)
	assert.True(t, whoami.Annotations.IdempotentHint)
	require.NotNil(t, whoami.Annotations.DestructiveHint, "DestructiveHint must be set explicitly (SDK default is true)")
	assert.False(t, *whoami.Annotations.DestructiveHint)
	assert.NotNil(t, whoami.OutputSchema, "whoami advertises its OpenAPI-derived output schema")
}
