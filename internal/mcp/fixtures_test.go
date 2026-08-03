package mcp

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/fixturetest"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// This file drives every fixture in FixtureTool through its mapped tool over a
// real in-memory MCP session — the MCP half of the shared-contract gate. For
// each fixture it asserts (a) the tool's structuredContent structurally equals
// the fixture's response body (null and absent equal, numbers coerced — the
// same fixturetest comparison internal/contract uses), (b) the outgoing HTTP
// request matches the fixture's documented method, path, query, and body, and
// (c) — via callTool — the result conforms to the tool's advertised output
// schema.

// fixtureRoundTrip describes how one contract fixture drives its mapped tool.
type fixtureRoundTrip struct {
	// args builds the tool arguments from the fixture (path coordinates,
	// query parameters, request body); nil calls the tool with no arguments.
	args func(t *testing.T, fx fixturetest.Fixture) map[string]any
	// stub registers the extra endpoints a composite tool calls besides the
	// fixture's own; nil for single-request tools.
	stub func(t *testing.T, reg *httpmock.Registry)
	// want builds the expected structured content; nil means the concretized
	// fixture response body passes through unwrapped.
	want func(t *testing.T, fx fixturetest.Fixture) any
	// requests is the expected number of captured API requests; 0 means 1.
	requests int
}

// stubFixture serves the fixture's concretized response body VERBATIM (the
// jsonBody responder, not httpmock.JSONResponse) for any request matching its
// method and placeholder-wildcarded path, so byte-exact passthrough stays
// observable end to end.
func stubFixture(reg *httpmock.Registry, fx fixturetest.Fixture) {
	reg.Register(
		func(req *http.Request) bool {
			return strings.EqualFold(req.Method, fx.Request.Method) &&
				fixturetest.PathMatches(fx.Request.Path, req.URL.Path)
		},
		jsonBody(fx.Response.Status, string(fixturetest.Concretize(fx.Response.Body))),
	)
}

// concretizedBody decodes the fixture's concretized response body into the
// generic tree a client sees as structuredContent.
func concretizedBody(t *testing.T, fx fixturetest.Fixture) any {
	t.Helper()
	return unmarshalAny(t, fixturetest.Concretize(fx.Response.Body))
}

// canonicalTree re-marshals v and canonicalizes it (nulls dropped, numbers
// coerced), so two trees compare structurally under the null≡absent rule.
func canonicalTree(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	tree, err := fixturetest.Canonicalize(raw)
	require.NoError(t, err)
	return tree
}

// repoArg reads the ACCOUNT/NAME repo argument out of the fixture path.
func repoArg(t *testing.T, fx fixturetest.Fixture) string {
	t.Helper()
	account, name := fixturetest.RepoCoords(fx.Request.Path)
	require.NotEmpty(t, account, "fixture path %s carries no repo coordinates", fx.Request.Path)
	require.NotEmpty(t, name, "fixture path %s carries no repo coordinates", fx.Request.Path)
	return account + "/" + name
}

// modelKeyArg reads the model key path parameter, concretizing its "<uuid>"
// placeholder into the sample value the fixture comparison expects.
func modelKeyArg(t *testing.T, fx fixturetest.Fixture) string {
	t.Helper()
	key := fixturetest.SegmentAfter(fx.Request.Path, "models")
	require.NotEmpty(t, key, "fixture path %s carries no model key", fx.Request.Path)
	return fixturetest.ConcretizeString(key)
}

// queryOf parses the fixture request path's query string.
func queryOf(t *testing.T, fx fixturetest.Fixture) url.Values {
	t.Helper()
	u, err := url.Parse(fx.Request.Path)
	require.NoError(t, err)
	return u.Query()
}

// pageArgs derives the tool's limit/offset arguments from the fixture query,
// so the outgoing page matches the documented request exactly.
func pageArgs(t *testing.T, fx fixturetest.Fixture) map[string]any {
	t.Helper()
	q := queryOf(t, fx)
	limit, err := strconv.Atoi(q.Get("limit"))
	require.NoError(t, err, "fixture query %s carries no numeric limit", fx.Request.Path)
	offset, err := strconv.Atoi(q.Get("offset"))
	require.NoError(t, err, "fixture query %s carries no numeric offset", fx.Request.Path)
	return map[string]any{"limit": limit, "offset": offset}
}

// repoAndModel is the argument pair most model tools take.
func repoAndModel(t *testing.T, fx fixturetest.Fixture) map[string]any {
	t.Helper()
	return map[string]any{"repo": repoArg(t, fx), "model_key": modelKeyArg(t, fx)}
}

// accountSection maps a get_account_info section fixture: include narrows the
// call to exactly this section, and the envelope names it.
func accountSection(key string) fixtureRoundTrip {
	return fixtureRoundTrip{
		args: func(t *testing.T, fx fixturetest.Fixture) map[string]any {
			return map[string]any{"include": []any{key}}
		},
		want: func(t *testing.T, fx fixturetest.Fixture) any {
			return map[string]any{key: concretizedBody(t, fx)}
		},
	}
}

// fixtureRoundTrips keys one round-trip recipe per FixtureTool fixture; the
// tool each fixture drives comes from FixtureTool itself.
// TestFixtureToolRoundTrips enforces that the two key sets match.
var fixtureRoundTrips = map[string]fixtureRoundTrip{
	"get_me":           {},
	"get_usage":        accountSection("usage"),
	"get_usage_quotas": accountSection("quotas"),
	"get_billing_plan": accountSection("plan"),
	"list_repos":       {args: pageArgs},
	"create_repo": {
		// name and model_type come from the fixture's documented request
		// body, so the outgoing body comparison stays fixture-driven.
		args: func(t *testing.T, fx fixturetest.Fixture) map[string]any {
			var body struct {
				Name      string `json:"name"`
				ModelType string `json:"model_type"`
			}
			require.NoError(t, json.Unmarshal(fixturetest.Concretize(fx.Request.Body), &body))
			return map[string]any{"name": body.Name, "model_type": body.ModelType}
		},
	},
	"get_repo": {
		args: func(t *testing.T, fx fixturetest.Fixture) map[string]any {
			return map[string]any{"repo": repoArg(t, fx)}
		},
	},
	"get_model": {args: repoAndModel},
	// The targets listing reaches a caller only inside get_model's
	// include_targets envelope, next to the model it belongs to.
	"list_model_targets": {
		args: func(t *testing.T, fx fixturetest.Fixture) map[string]any {
			args := repoAndModel(t, fx)
			args["include_targets"] = true
			return args
		},
		stub: func(t *testing.T, reg *httpmock.Registry) {
			stubFixture(reg, fixturetest.Load(t, "get_model"))
		},
		want: func(t *testing.T, fx fixturetest.Fixture) any {
			return map[string]any{
				"model":   concretizedBody(t, fixturetest.Load(t, "get_model")),
				"targets": concretizedBody(t, fx),
			}
		},
		requests: 2,
	},
	"get_model_status":  {args: repoAndModel},
	"set_default_model": {args: repoAndModel},
	"import_model": {
		args: func(t *testing.T, fx fixturetest.Fixture) map[string]any {
			var body struct {
				HfRepo string `json:"hf_repo"`
			}
			require.NoError(t, json.Unmarshal(fixturetest.Concretize(fx.Request.Body), &body))
			return map[string]any{"repo": repoArg(t, fx), "hf_repo": body.HfRepo}
		},
	},
	"get_deployment_options": {},
	"get_deployment_guide": {
		args: func(t *testing.T, fx fixturetest.Fixture) map[string]any {
			q := queryOf(t, fx)
			args := repoAndModel(t, fx)
			args["language"] = q.Get("language")
			args["inference_mode"] = q.Get("inference_mode")
			return args
		},
	},
	"get_general_report": {args: reportArgs},
	"get_llm_report":     {args: reportArgs},
	"get_package_report": {args: reportArgs},
	"list_library_models": {
		args: func(t *testing.T, fx fixturetest.Fixture) map[string]any {
			return pageArgs(t, fx)
		},
	},
	// The provider list reaches a caller only inside search_library's
	// include_providers envelope, next to a model page.
	"list_library_providers": {
		args: func(t *testing.T, fx fixturetest.Fixture) map[string]any {
			return map[string]any{"include_providers": true}
		},
		stub: func(t *testing.T, reg *httpmock.Registry) {
			stubFixture(reg, fixturetest.Load(t, "list_library_models"))
		},
		want: func(t *testing.T, fx fixturetest.Fixture) any {
			return map[string]any{
				"models":    concretizedBody(t, fixturetest.Load(t, "list_library_models")),
				"providers": concretizedBody(t, fx),
			}
		},
		requests: 2,
	},
	"get_library_model": {
		args: func(t *testing.T, fx fixturetest.Fixture) map[string]any {
			account, name := fixturetest.LibraryCoords(fx.Request.Path)
			require.NotEmpty(t, account)
			require.NotEmpty(t, name)
			return map[string]any{"library_model": account + "/" + name}
		},
	},
	// confirm passes the billing gate; include_urls keeps the response a
	// verbatim passthrough of the fixture — the default redaction rewrites
	// artifacts[].url and is pinned separately in tool_download_test.go.
	"create_download_authorization": {
		args: func(t *testing.T, fx fixturetest.Fixture) map[string]any {
			args := repoAndModel(t, fx)
			targetID := fixturetest.SegmentAfter(fx.Request.Path, "targets")
			require.NotEmpty(t, targetID, "fixture path %s carries no target id", fx.Request.Path)
			args["target_id"] = fixturetest.ConcretizeString(targetID)
			args["confirm"] = true
			args["include_urls"] = true
			return args
		},
	},
}

// reportArgs derives get_model_report arguments; report_type is the trailing
// path segment of the fixture's reports endpoint.
func reportArgs(t *testing.T, fx fixturetest.Fixture) map[string]any {
	t.Helper()
	reportType := fixturetest.SegmentAfter(fx.Request.Path, "reports")
	require.NotEmpty(t, reportType, "fixture path %s names no report type", fx.Request.Path)
	args := repoAndModel(t, fx)
	args["report_type"] = reportType
	return args
}

// findFixtureRequest returns the captured request that matches the fixture's
// method and placeholder-wildcarded path.
func findFixtureRequest(t *testing.T, fx fixturetest.Fixture, reqs []*http.Request) *http.Request {
	t.Helper()
	for _, req := range reqs {
		if strings.EqualFold(req.Method, fx.Request.Method) &&
			fixturetest.PathMatches(fx.Request.Path, req.URL.Path) {
			return req
		}
	}
	t.Fatalf("no captured request matches the fixture's %s %s",
		fx.Request.Method, fx.Request.Path)
	return nil
}

// assertRequestMatchesFixture checks the captured request against the
// fixture's documented request: query exactly, and — when the fixture carries
// one — the body structurally after concretization (the direction-b assertion
// internal/contract makes for the generated client, here made through the
// tool handler).
func assertRequestMatchesFixture(t *testing.T, fx fixturetest.Fixture, req *http.Request) {
	t.Helper()
	wantURL, err := url.Parse(fx.Request.Path)
	require.NoError(t, err)
	assert.Equal(t, wantURL.Query().Encode(), req.URL.Query().Encode(),
		"outgoing query must match the fixture request")
	if len(fx.Request.Body) == 0 || string(fx.Request.Body) == "null" {
		return
	}
	want, err := fixturetest.Canonicalize(fixturetest.Concretize(fx.Request.Body))
	require.NoError(t, err)
	got, err := fixturetest.Canonicalize([]byte(requestBody(t, req)))
	require.NoError(t, err)
	assert.Equal(t, want, got, "outgoing request body must match the fixture request")
}

// TestFixtureToolRoundTrips drives each mapped contract fixture through its
// tool over a real in-memory MCP session and checks both directions plus
// schema conformance (see the file comment).
func TestFixtureToolRoundTrips(t *testing.T) {
	for stem, tool := range FixtureTool {
		tc, ok := fixtureRoundTrips[stem]
		require.True(t, ok, "mapped fixture %s has no round-trip case in fixtures_test.go", stem)
		t.Run(stem, func(t *testing.T) {
			fx := fixturetest.Load(t, stem)
			reg := &httpmock.Registry{}
			stubFixture(reg, fx)
			if tc.stub != nil {
				tc.stub(t, reg)
			}

			cs, _ := connect(t, registryProvider(t, reg))
			var args map[string]any
			if tc.args != nil {
				args = tc.args(t, fx)
			}
			res := callTool(t, cs, tool, args) // validates the output schema too
			if res.IsError {
				t.Fatalf("%s returned a tool error for the fixture exchange: %s",
					tool, textOf(t, res))
			}

			// (a) The structured content is the fixture's response body —
			// wrapped in its documented envelope where the tool composes one —
			// under the shared null≡absent comparison.
			want := concretizedBody(t, fx)
			if tc.want != nil {
				want = tc.want(t, fx)
			}
			assert.Equal(t, canonicalTree(t, want), canonicalTree(t, res.StructuredContent),
				"%s structuredContent must structurally equal the %s fixture", tool, stem)

			// (b) The outgoing request is the fixture's documented request.
			wantRequests := tc.requests
			if wantRequests == 0 {
				wantRequests = 1
			}
			require.Len(t, reg.Requests, wantRequests,
				"%s must make exactly the documented API requests", tool)
			assertRequestMatchesFixture(t, fx, findFixtureRequest(t, fx, reg.Requests))
			reg.Verify(t)
		})
	}
}

// TestFixtureRoundTripTableMatchesFixtureTool pins the two key sets to each
// other: a stale round-trip case must be removed alongside its mapping (the
// reverse direction — every mapping has a case — fails TestFixtureToolRoundTrips).
func TestFixtureRoundTripTableMatchesFixtureTool(t *testing.T) {
	for stem := range fixtureRoundTrips {
		_, ok := FixtureTool[stem]
		assert.True(t, ok, "round-trip case %s matches no FixtureTool entry", stem)
	}
}

// TestFixtureToolNamesOnlyRegisteredTools keeps the exported mapping honest: a
// renamed or dropped tool must not leave FixtureTool pointing at nothing.
func TestFixtureToolNamesOnlyRegisteredTools(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	registered := map[string]bool{}
	for _, tool := range listAllTools(t, cs) {
		registered[tool.Name] = true
	}
	for stem, tool := range FixtureTool {
		assert.True(t, registered[tool],
			"FixtureTool maps fixture %s to unregistered tool %s", stem, tool)
	}
}
