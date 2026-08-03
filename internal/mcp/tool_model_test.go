package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// Bodies use non-alphabetical key order so any re-marshal through a typed
// struct (which would sort keys) breaks the byte-equality assertions.
//
// The `<` and `&` characters are equally deliberate: descriptions are prose
// and carry them, and json.Marshal rewrites them as < and &
// whenever it re-emits a json.RawMessage. An envelope built with HTML
// escaping fails on these bodies.
const (
	modelListBody = `{"results":[{"key":"whisper-tiny-1","version":1,"type":"onnx","state":"ready",` +
		`"is_default":true,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}],"count":1}`
	modelBody = `{"key":"whisper-tiny-1","version":1,"type":"onnx","state":"ready","is_default":true,` +
		`"terminal":true,"download_ready":true,"source_type":"manual",` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z",` +
		`"description":"speech <-> text & subtitles"}`
	targetsBody = `{"results":[{"target_id":"tgt_abc","kind":"general","target":"cpu","quant_type":"q4_k_m",` +
		`"download_size":5368709120,"created_at":"2026-01-01T00:00:00Z",` +
		`"label":"CPU <fp16> & NPU"}],"count":1}`
	importedModelBody = `{"key":"llama-3-2-1b-1","version":1,"state":"converting","is_default":false,` +
		`"source":"hf:meta-llama/Llama-3.2-1B","description":"instruct <chat> & tools"}`
)

// statusBody renders a model status response; terminal drives the poll loop.
func statusBody(state string, terminal bool) string {
	return `{"state":"` + state + `","stage":"convert","terminal":` +
		map[bool]string{true: "true", false: "false"}[terminal] +
		`,"download_ready":false,"failure_code":null,"progress":null,"retry_after":null,` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:01Z"}`
}

const statusPath = "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1/status"

// fakeClock is the deterministic clock/sleeper injected into wait.Poll: the
// polling tests must exercise the real backoff schedule without ever
// sleeping. Sleeping advances the clock instead of blocking.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	slept   []time.Duration
	onSleep func()
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	hook := c.onSleep
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return ctx.Err()
}

func (c *fakeClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

// usePollHooks installs a deterministic clock plus an identity jitter for the
// duration of the test. The restore is registered before any server session
// exists, so it runs after that session has been torn down.
func usePollHooks(t *testing.T) *fakeClock {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	t.Cleanup(func() { pollJitter, pollSleep, pollNow = nil, nil, nil })
	pollJitter = func(d time.Duration) time.Duration { return d } // no randomness
	pollSleep = clock.Sleep
	pollNow = clock.Now
	return clock
}

func TestListModelsPassesResponseBytesThroughAndDefaultsThePage(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models"),
		httpmock.JSONResponse(200, json.RawMessage(modelListBody)))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "list_models", map[string]any{"repo": "zetic/whisper-tiny"})

	assert.False(t, res.IsError)
	assert.Equal(t, modelListBody, textOf(t, res))

	query := reg.Requests[0].URL.Query()
	assert.Equal(t, "30", query.Get("limit"), "an omitted limit takes the default page size")
	assert.Equal(t, "0", query.Get("offset"))

	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+modelListBody)
	reg.Verify(t)
}

func TestListModelsForwardsPagination(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models"),
		httpmock.JSONResponse(200, json.RawMessage(modelListBody)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "list_models", map[string]any{
		"repo": "zetic/whisper-tiny", "limit": 5, "offset": 10,
	})
	assert.False(t, res.IsError)

	query := reg.Requests[0].URL.Query()
	assert.Equal(t, "5", query.Get("limit"))
	assert.Equal(t, "10", query.Get("offset"))
	reg.Verify(t)
}

func TestListModelsRejectsOversizedPageBeforeCallingTheAPI(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	// The bounds must reach the client, not just the handler. Asserting on the
	// advertised schema is what makes this test fail if withPageBounds is
	// dropped: an unbounded limit would otherwise reach the empty registry,
	// which reports a transport error that also surfaces as IsError.
	schema, err := json.Marshal(toolNamed(t, cs, "list_models").InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"maximum":100`, "the page size cap is advertised")
	assert.Contains(t, string(schema), `"minimum":1`, "a page of zero rows is refused")

	res := callTool(t, cs, "list_models", map[string]any{"repo": "zetic/whisper-tiny", "limit": 101})

	assert.True(t, res.IsError)
	// The SDK prefixes schema-validation failures this way; without it, an
	// IsError result could just as well be the unmatched-stub transport error.
	assert.Contains(t, textOf(t, res), `validating "arguments"`,
		"the argument is rejected by the schema, not by a failed request")
	assert.Empty(t, reg.Requests)
}

func TestListModelsInvalidRepoArgumentIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	res := callTool(t, cs, "list_models", map[string]any{"repo": "whisper-tiny"})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "ACCOUNT/NAME")
	assert.Empty(t, reg.Requests)
}

func TestGetModelPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1"),
		jsonBody(200, modelBody))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_model", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
	})

	assert.False(t, res.IsError)
	assert.Equal(t, modelBody, textOf(t, res))

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, modelBody)
	reg.Verify(t)
}

func TestGetModelWithoutIncludeTargetsSkipsTheTargetsCall(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1"),
		jsonBody(200, modelBody))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_model", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "include_targets": false,
	})

	assert.False(t, res.IsError)
	require.Len(t, reg.Requests, 1, "targets are fetched only when asked for")
	reg.Verify(t)
}

func TestGetModelIncludeTargetsEmitsCompositeEnvelope(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1"),
		jsonBody(200, modelBody))
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1/targets"),
		jsonBody(200, targetsBody))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_model", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "include_targets": true,
	})

	assert.False(t, res.IsError)
	want := `{"model":` + modelBody + `,"targets":` + targetsBody + `}`
	assert.Equal(t, want, textOf(t, res),
		"the envelope names both halves and keeps each response's bytes intact, "+
			"including the < and & an escaping re-marshal would rewrite")

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, want)
	reg.Verify(t)
}

func TestGetModelIncludeTargetsSurfacesTheTargetsFailure(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1"),
		jsonBody(200, modelBody))
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1/targets"),
		httpmock.JSONResponse(http.StatusForbidden, json.RawMessage(
			`{"type":"error","error":{"type":"permission_error","message":"token cannot read targets"},"request_id":"req_3"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_model", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "include_targets": true,
	})

	assert.True(t, res.IsError, "a half-built envelope is never returned as success")
	text := textOf(t, res)
	assert.Contains(t, text, "token cannot read targets")
	assert.Contains(t, text, "melange auth status")
	reg.Verify(t)
}

func TestGetConversionStatusWithoutWaitReadsOnce(t *testing.T) {
	body := statusBody("converting", false)
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", statusPath), httpmock.JSONResponse(200, json.RawMessage(body)))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_conversion_status", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
	})

	assert.False(t, res.IsError, "a non-terminal state is a valid answer, not a failure")
	assert.Equal(t, body, textOf(t, res))
	assert.Len(t, reg.Requests, 1, "wait_seconds 0 never polls")

	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+body)
	reg.Verify(t)
}

func TestGetConversionStatusPollsWithBackoffUntilTerminal(t *testing.T) {
	clock := usePollHooks(t)

	terminal := statusBody("ready", true)
	reg := &httpmock.Registry{}
	for _, body := range []string{statusBody("converting", false), statusBody("optimizing", false), terminal} {
		reg.Register(httpmock.REST("GET", statusPath), httpmock.JSONResponse(200, json.RawMessage(body)))
	}

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_conversion_status", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "wait_seconds": 60,
	})

	assert.False(t, res.IsError)
	assert.Equal(t, terminal, textOf(t, res), "polling returns the terminal status, not the first read")
	assert.Equal(t, []time.Duration{2 * time.Second, 3 * time.Second}, clock.sleeps(),
		"backoff starts at 2s and grows by 1.5x; it stops as soon as the state is terminal")
	reg.Verify(t)
}

func TestGetConversionStatusExhaustedBudgetReturnsTheLatestStatus(t *testing.T) {
	clock := usePollHooks(t)

	// Budget 10s against the 2s/1.5x schedule: polls land at t=0, 2, 5, 9.5
	// and a final clamped poll at the 10s deadline.
	last := statusBody("optimizing", false)
	reg := &httpmock.Registry{}
	for range 4 {
		reg.Register(httpmock.REST("GET", statusPath),
			httpmock.JSONResponse(200, json.RawMessage(statusBody("converting", false))))
	}
	reg.Register(httpmock.REST("GET", statusPath), httpmock.JSONResponse(200, json.RawMessage(last)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_conversion_status", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "wait_seconds": 10,
	})

	assert.False(t, res.IsError, "an exhausted wait budget is not an error")
	assert.Equal(t, last, textOf(t, res), "the caller still gets the freshest status it can act on")
	assert.Equal(t,
		[]time.Duration{2 * time.Second, 3 * time.Second, 4500 * time.Millisecond, 500 * time.Millisecond},
		clock.sleeps(), "the last sleep is clamped so a final poll runs at the deadline")
	reg.Verify(t)
}

func TestGetConversionStatusCanceledContextIsToolError(t *testing.T) {
	clock := usePollHooks(t)

	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", statusPath),
		httpmock.JSONResponse(200, json.RawMessage(statusBody("converting", false))))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock.onSleep = cancel // the caller goes away while the tool waits

	// Called directly: cancellation must be observed on the handler's own
	// context, which an in-memory client session does not let a test hold.
	handler := getConversionStatusHandler(Deps{Clients: registryProvider(t, reg)})
	res, _, err := handler(ctx, nil, conversionStatusArgs{
		Repo: "zetic/whisper-tiny", ModelKey: "whisper-tiny-1", WaitSeconds: 60,
	})

	require.NoError(t, err, "cancellation is a tool error, not a protocol error")
	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), context.Canceled.Error())
	reg.Verify(t)
}

func TestGetConversionStatusAPIErrorDuringPollingIsToolError(t *testing.T) {
	usePollHooks(t)

	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", statusPath),
		httpmock.JSONResponse(200, json.RawMessage(statusBody("converting", false))))
	reg.Register(httpmock.REST("GET", statusPath),
		httpmock.JSONResponse(http.StatusUnauthorized, json.RawMessage(
			`{"type":"error","error":{"type":"authentication_error","message":"token expired"},"request_id":"req_7"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_conversion_status", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "wait_seconds": 60,
	})

	assert.True(t, res.IsError, "a stale status is not returned in place of a real failure")
	text := textOf(t, res)
	assert.Contains(t, text, "token expired")
	assert.Contains(t, text, "melange auth login")
	assert.Contains(t, text, "MELANGE_API_KEY")
	reg.Verify(t)
}

func TestGetConversionStatusRejectsAnOverlongWaitBeforeCallingTheAPI(t *testing.T) {
	for _, tc := range []struct {
		name        string
		waitSeconds int
	}{
		{"above the cap", maxWaitSeconds + 1},
		{"negative", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "get_conversion_status", map[string]any{
				"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
				"wait_seconds": tc.waitSeconds,
			})

			assert.True(t, res.IsError, "the schema caps the wait budget at %ds", maxWaitSeconds)
			assert.Empty(t, reg.Requests)
		})
	}
}

func TestSetDefaultModelPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("PUT", "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1/default"),
		jsonBody(200, modelBody))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "set_default_model", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
	})

	assert.False(t, res.IsError)
	assert.Equal(t, modelBody, textOf(t, res))
	require.Len(t, reg.Requests, 1)
	assert.Equal(t, http.MethodPut, reg.Requests[0].Method)

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, modelBody)
	reg.Verify(t)
}

func TestSetDefaultModelInvalidRepoArgumentIsToolErrorWithoutAnAPICall(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	res := callTool(t, cs, "set_default_model", map[string]any{
		"repo": "whisper-tiny", "model_key": "whisper-tiny-1",
	})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "ACCOUNT/NAME")
	assert.Empty(t, reg.Requests, "a malformed repo never reaches the API")
}

func TestSetDefaultModelAPIFailureIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("PUT", "/v1/repos/zetic/whisper-tiny/models/nope/default"),
		httpmock.JSONResponse(http.StatusNotFound, json.RawMessage(
			`{"type":"error","error":{"type":"not_found_error","message":"model not found"},"request_id":"req_31"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "set_default_model", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "nope",
	})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "model not found")
	reg.Verify(t)
}

func TestImportModelSendsTheHuggingFaceRepoAndPassesResponseBytesThrough(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/llama/models/import"),
		jsonBody(http.StatusCreated, importedModelBody))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "import_model", map[string]any{
		"repo": "zetic/llama", "hf_repo": "meta-llama/Llama-3.2-1B",
	})

	assert.False(t, res.IsError)
	assert.Equal(t, importedModelBody, textOf(t, res))
	require.Len(t, reg.Requests, 1)
	// revision is reserved and rejected when non-null, so the tool must not
	// invent one.
	assert.JSONEq(t, `{"hf_repo":"meta-llama/Llama-3.2-1B"}`, requestBody(t, reg.Requests[0]))

	require.NoError(t, cs.Close())
	assertStructuredContentOnWire(t, wire, importedModelBody)
	reg.Verify(t)
}

func TestImportModelCarriesAFreshIdempotencyKeyPerCall(t *testing.T) {
	reg := &httpmock.Registry{}
	for range 2 {
		reg.Register(httpmock.REST("POST", "/v1/repos/zetic/llama/models/import"),
			jsonBody(http.StatusCreated, importedModelBody))
	}

	cs, _ := connect(t, registryProvider(t, reg))
	for range 2 {
		assert.False(t, callTool(t, cs, "import_model", map[string]any{
			"repo": "zetic/llama", "hf_repo": "meta-llama/Llama-3.2-1B",
		}).IsError)
	}

	require.Len(t, reg.Requests, 2)
	first := reg.Requests[0].Header.Get("Idempotency-Key")
	second := reg.Requests[1].Header.Get("Idempotency-Key")
	// The key is what lets the transport replay one import after a transient
	// failure; sharing it between calls would instead make a deliberate second
	// import replay the first one's outcome.
	assert.NotEmpty(t, first, "an import must be replay-safe within its own call")
	assert.NotEqual(t, first, second, "each call starts a new import, so each carries a new key")
	reg.Verify(t)
}

func TestImportModelAPIFailureIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("POST", "/v1/repos/zetic/whisper-tiny/models/import"),
		httpmock.JSONResponse(http.StatusUnprocessableEntity, json.RawMessage(
			`{"type":"error","error":{"type":"invalid_request_error","message":"repository does not accept imports",`+
				`"fields":[{"field":"model_type","message":"must be llm"}]},"request_id":"req_33"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "import_model", map[string]any{
		"repo": "zetic/whisper-tiny", "hf_repo": "meta-llama/Llama-3.2-1B",
	})

	assert.True(t, res.IsError)
	text := textOf(t, res)
	assert.Contains(t, text, "repository does not accept imports")
	assert.Contains(t, text, "model_type: must be llm", "field-level detail reaches the agent")
	reg.Verify(t)
}

func TestModelWriteToolAnnotations(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	assertMutatingAnnotations(t, cs, "set_default_model", false, true)
	assertMutatingAnnotations(t, cs, "import_model", false, false)

	// import_model reaches HuggingFace, not just the Melange API.
	openWorld := toolNamed(t, cs, "import_model").Annotations.OpenWorldHint
	require.NotNil(t, openWorld, "import_model declares that it touches a third-party system")
	assert.True(t, *openWorld)
}

func TestImportModelAdvertisesTheAsyncFollowUp(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	tool := toolNamed(t, cs, "import_model")

	// The description is the only place an agent learns that the returned
	// model is not ready yet and which tool to poll.
	assert.Contains(t, tool.Description, "get_conversion_status")
	assert.Contains(t, tool.Description, "Returns immediately")
}

func TestModelToolAnnotations(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	for _, name := range []string{"list_models", "get_model", "get_conversion_status"} {
		t.Run(name, func(t *testing.T) { assertReadOnlyAnnotations(t, cs, name) })
	}
}

func TestGetConversionStatusAdvertisesItsPollingContract(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	tool := toolNamed(t, cs, "get_conversion_status")

	// The description is the only place an agent learns that a timed-out wait
	// still yields a usable status, so it must keep saying so.
	assert.Contains(t, tool.Description, "wait_seconds")
	assert.Contains(t, tool.Description, "call again")

	schema, err := json.Marshal(tool.InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"maximum":120`,
		"the wait budget cap must reach the client, not just the handler")
}
