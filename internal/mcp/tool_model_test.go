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
const (
	modelListBody = `{"results":[{"key":"whisper-tiny-1","version":1,"type":"onnx","is_default":true}],"count":1}`
	modelBody     = `{"key":"whisper-tiny-1","version":1,"type":"onnx","state":"ready","is_default":true}`
	targetsBody   = `{"results":[{"target_id":"tgt_abc","target":"cpu","quant_type":"q4_k_m"}],"count":1}`
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

	res := callTool(t, cs, "list_models", map[string]any{"repo": "zetic/whisper-tiny", "limit": 101})

	assert.True(t, res.IsError)
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
		httpmock.JSONResponse(200, json.RawMessage(modelBody)))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_model", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
	})

	assert.False(t, res.IsError)
	assert.Equal(t, modelBody, textOf(t, res))

	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+modelBody)
	reg.Verify(t)
}

func TestGetModelWithoutIncludeTargetsSkipsTheTargetsCall(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1"),
		httpmock.JSONResponse(200, json.RawMessage(modelBody)))

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
		httpmock.JSONResponse(200, json.RawMessage(modelBody)))
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1/targets"),
		httpmock.JSONResponse(200, json.RawMessage(targetsBody)))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_model", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "include_targets": true,
	})

	assert.False(t, res.IsError)
	want := `{"model":` + modelBody + `,"targets":` + targetsBody + `}`
	assert.Equal(t, want, textOf(t, res),
		"the envelope names both halves and keeps each response's bytes intact")

	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+want)
	reg.Verify(t)
}

func TestGetModelIncludeTargetsSurfacesTheTargetsFailure(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1"),
		httpmock.JSONResponse(200, json.RawMessage(modelBody)))
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
