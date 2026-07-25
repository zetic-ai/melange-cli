// Package contract verifies that the melange CLI's GENERATED OpenAPI client
// round-trips the shared contract fixtures produced by the backend.
//
// The fixtures under openapi/fixtures/ are the single ground truth the backend
// (its real TestClient responses), this CLI, and the python SDK all agree on;
// see openapi/FIXTURES_SOURCE for the source commit. This test is the CLI half
// of the M4 gate ("CLI, python SDK, and raw OpenAPI requests pass shared
// contract fixtures"): for every covered fixture it checks BOTH directions.
//
//   - Response side: decode the fixture's response body through the generated
//     response type, re-marshal it, and compare structurally. A field the
//     generated struct silently drops (an unmodeled/renamed field) leaves the
//     re-marshaled tree missing that key — which fails the comparison. So the
//     test catches contract drift where the CLI's types no longer capture what
//     the backend sends.
//
//   - Request side: drive the matching generated client call through the
//     httpmock registry and assert the outgoing method/path/body match the
//     fixture's request (placeholders — <id>, <uuid>, ... — are compared
//     structurally, since they stand in for per-run values).
//
// Fixtures carry placeholder tokens for volatile values; typed Go fields
// (time.Time, enums) cannot unmarshal a literal "<datetime>", so the tokens
// are first concretized to type-valid sample values (concretize) before the
// round-trip. The comparison treats JSON null and an absent key as equal so a
// nullable field rendered null by the backend but `omitempty`-dropped by the
// generated struct is not a false positive.
package contract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

const (
	testHost = "https://api.zetic.ai"
	testUA   = "melange-cli/test (contract)"
)

// ---------------------------------------------------------------------------
// fixture types
// ---------------------------------------------------------------------------

type fixture struct {
	Request  fixtureRequest  `json:"request"`
	Response fixtureResponse `json:"response"`
}

type fixtureRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

type fixtureResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func fixturesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate caller for fixtures dir")
	}
	// internal/contract/fixtures_test.go -> repo root.
	root := filepath.Join(filepath.Dir(file), "..", "..")
	dir := filepath.Join(root, "openapi", "fixtures")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fixtures dir %s: %v", dir, err)
	}
	return dir
}

func loadFixture(t *testing.T, name string) fixture {
	t.Helper()
	path := filepath.Join(fixturesDir(t), name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	var fx fixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("decoding fixture %s: %v", name, err)
	}
	return fx
}

// ---------------------------------------------------------------------------
// placeholder concretization + structural comparison
// ---------------------------------------------------------------------------

// concreteFor maps each normalization placeholder to a type-valid sample the
// generated structs can unmarshal (time.Time, enums, plain strings). The value
// only has to be well-formed for its Go type; the round-trip compares the
// concretized input against the re-marshaled output, so the exact sample is
// irrelevant as long as it survives the round-trip unchanged.
var concreteFor = map[string]string{
	"<id>":         "00000000000000000000000000000000",
	"<uuid>":       "00000000000000000000000000000000",
	"<datetime>":   "2026-01-01T00:00:00Z",
	"<request_id>": "req_sample",
	"<signed-url>": "https://storage.example/sample?sig=1",
	"<target_id>":  "tm_1",
}

// concretize replaces every placeholder token that appears inside JSON string
// values with its type-valid sample, so typed Go fields can unmarshal it.
// Placeholders only ever occupy (or are embedded in) JSON strings, so a plain
// token substitution on the raw JSON text is exact and reversible.
func concretize(raw json.RawMessage) json.RawMessage {
	s := string(raw)
	for token, sample := range concreteFor {
		s = strings.ReplaceAll(s, token, sample)
	}
	return json.RawMessage(s)
}

// canonicalize decodes JSON into a generic tree with nulls dropped and every
// number coerced to float64, so the comparison treats (a) an explicit null and
// an absent key as equal — a nullable field the backend renders null but a
// generated `omitempty` field drops — and (b) numeric-representation artifacts
// as equal — JSON has no int/float distinction, so a fixture `1.0` and Go's
// re-marshaled `1` must compare equal.
func canonicalize(raw []byte) (any, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return dropNulls(v), nil
}

func dropNulls(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = dropNulls(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = dropNulls(val)
		}
		return out
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return t
		}
		return f
	default:
		return v
	}
}

// roundTrip unmarshals concretized fixture body JSON into dst (a pointer to a
// generated type), re-marshals it, and asserts the re-marshaled tree matches
// the concretized input (nulls/absent treated equal). A field the generated
// type does not model is dropped on re-marshal and surfaces here as a diff.
func roundTrip(t *testing.T, name string, body json.RawMessage, dst any) {
	t.Helper()
	concrete := concretize(body)

	// Strict decode: an unknown field in the fixture that the generated type
	// does not model is itself a contract gap (the CLI would silently ignore
	// backend data), so fail loudly rather than drop it.
	dec := json.NewDecoder(bytes.NewReader(concrete))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		t.Fatalf("%s: generated type %T does not model the fixture body: %v",
			name, dst, err)
	}

	remarshaled, err := json.Marshal(dst)
	if err != nil {
		t.Fatalf("%s: re-marshaling %T: %v", name, dst, err)
	}

	want, err := canonicalize(concrete)
	if err != nil {
		t.Fatalf("%s: canonicalizing fixture body: %v", name, err)
	}
	got, err := canonicalize(remarshaled)
	if err != nil {
		t.Fatalf("%s: canonicalizing re-marshaled body: %v", name, err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s: response body did not round-trip through %T\n want: %s\n  got: %s",
			name, dst, mustJSON(want), mustJSON(got))
	}
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// ---------------------------------------------------------------------------
// request-side driver
// ---------------------------------------------------------------------------

// driveRequest builds a generated client whose transport is the httpmock
// registry, invokes call (which performs one generated operation), and returns
// the single captured outgoing request. The stubbed response is the fixture's
// own concretized response so the generated WithResponse call succeeds.
func driveRequest(
	t *testing.T,
	fx fixture,
	call func(ctx context.Context, c *gen.ClientWithResponses) error,
) *http.Request {
	t.Helper()
	reg := &httpmock.Registry{}
	stubPath := placeholderPathToWildcard(fx.Request.Path)
	reg.Register(
		func(req *http.Request) bool {
			return strings.EqualFold(req.Method, fx.Request.Method) &&
				pathMatches(stubPath, req.URL.Path)
		},
		httpmock.JSONResponse(fx.Response.Status, concretize(fx.Response.Body)),
	)

	client, err := api.NewClient(api.Options{
		Host: testHost, Token: "ztp_test", UserAgent: testUA, Transport: reg,
	})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}
	g, err := client.Gen()
	if err != nil {
		t.Fatalf("building generated client: %v", err)
	}
	if err := call(context.Background(), g); err != nil {
		t.Fatalf("driving generated call: %v", err)
	}
	if len(reg.Requests) != 1 {
		t.Fatalf("expected exactly 1 captured request, got %d", len(reg.Requests))
	}
	return reg.Requests[0]
}

// pathSegments splits a URL path (dropping any query string) into segments.
func pathSegments(p string) []string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	return strings.Split(strings.Trim(p, "/"), "/")
}

// placeholderPathToWildcard leaves the fixture path as-is; placeholder segments
// (e.g. "<uuid>") are treated as wildcards by pathMatches.
func placeholderPathToWildcard(p string) string { return p }

// pathMatches compares a fixture path (whose variable segments are placeholder
// tokens like "<uuid>") against an actual path segment-by-segment, treating any
// "<...>" fixture segment as a wildcard.
func pathMatches(fixturePath, actualPath string) bool {
	want := pathSegments(fixturePath)
	got := pathSegments(actualPath)
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if strings.HasPrefix(want[i], "<") && strings.HasSuffix(want[i], ">") {
			continue // wildcard segment
		}
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

// assertRequest checks the captured outgoing request against the fixture's
// request: method, path (placeholders wildcarded), and — for bodies — a
// structural comparison after concretizing the fixture body.
func assertRequest(t *testing.T, name string, fx fixture, got *http.Request) {
	t.Helper()
	if !strings.EqualFold(got.Method, fx.Request.Method) {
		t.Fatalf("%s: request method = %s, want %s", name, got.Method, fx.Request.Method)
	}
	if !pathMatches(fx.Request.Path, got.URL.Path) {
		t.Fatalf("%s: request path = %s, want %s (placeholders wildcarded)",
			name, got.URL.Path, fx.Request.Path)
	}
	wantURL, err := url.Parse(fx.Request.Path)
	if err != nil {
		t.Fatalf("%s: parsing fixture request URL: %v", name, err)
	}
	if wantURL.Query().Encode() != got.URL.Query().Encode() {
		t.Fatalf("%s: request query = %q, want %q", name, got.URL.RawQuery, wantURL.RawQuery)
	}
	if len(fx.Request.Body) == 0 || string(fx.Request.Body) == "null" {
		return
	}
	gotBody, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatalf("%s: reading captured request body: %v", name, err)
	}
	want, err := canonicalize(concretize(fx.Request.Body))
	if err != nil {
		t.Fatalf("%s: canonicalizing fixture request body: %v", name, err)
	}
	gotTree, err := canonicalize(gotBody)
	if err != nil {
		t.Fatalf("%s: canonicalizing captured request body: %v", name, err)
	}
	if !reflect.DeepEqual(want, gotTree) {
		t.Fatalf("%s: request body mismatch\n want: %s\n  got: %s",
			name, mustJSON(want), mustJSON(gotTree))
	}
}

// ---------------------------------------------------------------------------
// per-operation coverage table
// ---------------------------------------------------------------------------

// segAfter returns the path segment immediately following marker in the
// fixture path (e.g. the account name after "repos"), or "" if absent.
func segAfter(p, marker string) string {
	segs := pathSegments(p)
	for i, s := range segs {
		if s == marker && i+1 < len(segs) {
			return segs[i+1]
		}
	}
	return ""
}

// repoCoords pulls the {account, repo} pair out of a "/v1/repos/{a}/{r}/..."
// fixture path. The values are placeholder-free literals from the fixtures
// (stable account/repo names), so they drive the generated path builders.
func repoCoords(p string) (account, repo string) {
	segs := pathSegments(p)
	for i, s := range segs {
		if s == "repos" && i+2 < len(segs) {
			return segs[i+1], segs[i+2]
		}
	}
	return "", ""
}

// modelKey / uploadId pull the trailing key out of the relevant subpaths.
func afterModels(p string) string { return segAfter(p, "models") }
func uploadID(p string) string    { return segAfter(p, "uploads") }

// targetID pulls the opaque target id out of a
// "/models/{key}/targets/{target_id}/download-authorizations" path.
func targetID(p string) string { return segAfter(p, "targets") }

// libraryCoords pulls the {account, repo} pair out of a
// "/v1/library/models/{account}/{repo}" fixture path (get_library_model). The
// repos-based repoCoords does not apply to the library namespace.
func libraryCoords(p string) (account, repo string) {
	segs := pathSegments(p)
	for i, s := range segs {
		if s == "models" && i+2 < len(segs) {
			return segs[i+1], segs[i+2]
		}
	}
	return "", ""
}

// contractCase couples a fixture with the response type to round-trip and the
// generated call to drive for its request. Either half may be nil when the
// fixture is response-only (error envelopes) or has no client method wired.
type contractCase struct {
	name         string
	responseBody func() any
	drive        func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error
}

func cases() []contractCase {
	return []contractCase{
		{
			name:         "get_me",
			responseBody: func() any { return &gen.MeResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				_, err := c.GetMeWithResponse(ctx)
				return err
			},
		},
		{
			name:         "create_repo",
			responseBody: func() any { return &gen.RepoResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				var body gen.CreateRepoJSONRequestBody
				if err := json.Unmarshal(concretize(fx.Request.Body), &body); err != nil {
					return err
				}
				_, err := c.CreateRepoWithResponse(ctx, body)
				return err
			},
		},
		{
			name:         "get_repo",
			responseBody: func() any { return &gen.RepoResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.GetRepoWithResponse(ctx, a, r)
				return err
			},
		},
		{
			name:         "list_repos",
			responseBody: func() any { return &gen.PagedRepoResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				limit, offset := 20, 0
				_, err := c.ListReposWithResponse(ctx, &gen.ListReposParams{
					Limit: &limit, Offset: &offset,
				})
				return err
			},
		},
		{
			name:         "get_deployment_options",
			responseBody: func() any { return &gen.DeploymentOptionsResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				_, err := c.GetDeploymentOptionsWithResponse(ctx)
				return err
			},
		},
		{
			name:         "create_model_upload",
			responseBody: func() any { return &gen.ModelUploadResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				var body gen.CreateModelUploadJSONRequestBody
				if err := json.Unmarshal(concretize(fx.Request.Body), &body); err != nil {
					return err
				}
				_, err := c.CreateModelUploadWithResponse(
					ctx, a, r, &gen.CreateModelUploadParams{}, body)
				return err
			},
		},
		{
			name:         "get_model_upload",
			responseBody: func() any { return &gen.ModelUploadDetailResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.GetModelUploadWithResponse(ctx, a, r, uploadID(fx.Request.Path))
				return err
			},
		},
		{
			name:         "cancel_model_upload",
			responseBody: func() any { return &gen.CancelModelUploadResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.CancelModelUploadWithResponse(
					ctx, a, r, uploadID(fx.Request.Path), &gen.CancelModelUploadParams{})
				return err
			},
		},
		{
			name:         "complete_model_upload",
			responseBody: func() any { return &gen.CompleteModelUploadResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.CompleteModelUploadWithResponse(
					ctx, a, r, uploadID(fx.Request.Path), &gen.CompleteModelUploadParams{})
				return err
			},
		},
		{
			name:         "get_model_status",
			responseBody: func() any { return &gen.ModelStatusResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.GetModelStatusWithResponse(ctx, a, r, afterModels(fx.Request.Path))
				return err
			},
		},
		{
			name:         "get_model",
			responseBody: func() any { return &gen.ModelDetailResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.GetModelWithResponse(ctx, a, r, afterModels(fx.Request.Path))
				return err
			},
		},
		{
			name:         "get_deployment_guide",
			responseBody: func() any { return &gen.DeploymentGuideResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				query, err := url.Parse(fx.Request.Path)
				if err != nil {
					return err
				}
				language := gen.GetDeploymentGuideParamsLanguage(query.Query().Get("language"))
				mode := gen.GetDeploymentGuideParamsInferenceMode(query.Query().Get("inference_mode"))
				_, err = c.GetDeploymentGuideWithResponse(
					ctx, a, r, afterModels(fx.Request.Path), &gen.GetDeploymentGuideParams{
						Language: &language, InferenceMode: &mode,
					},
				)
				return err
			},
		},
		{
			name:         "list_model_targets",
			responseBody: func() any { return &gen.ListModelTargetsResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.ListModelTargetsWithResponse(ctx, a, r, afterModels(fx.Request.Path))
				return err
			},
		},
		{
			name:         "get_general_report",
			responseBody: func() any { return &gen.GeneralReportResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.GetGeneralReportWithResponse(ctx, a, r, afterModels(fx.Request.Path))
				return err
			},
		},
		{
			name:         "get_llm_report",
			responseBody: func() any { return &gen.LlmReportResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.GetLlmReportWithResponse(ctx, a, r, afterModels(fx.Request.Path))
				return err
			},
		},
		{
			name:         "get_package_report",
			responseBody: func() any { return &gen.PackageReportResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.GetPackageReportWithResponse(ctx, a, r, afterModels(fx.Request.Path))
				return err
			},
		},
		{
			name:         "set_default_model",
			responseBody: func() any { return &gen.ModelSummary{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.SetDefaultModelWithResponse(ctx, a, r, afterModels(fx.Request.Path))
				return err
			},
		},
		{
			name:         "import_model",
			responseBody: func() any { return &gen.ImportModelResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				var body gen.ImportModelJSONRequestBody
				if err := json.Unmarshal(concretize(fx.Request.Body), &body); err != nil {
					return err
				}
				_, err := c.ImportModelWithResponse(ctx, a, r, &gen.ImportModelParams{}, body)
				return err
			},
		},
		{
			name:         "create_download_authorization",
			responseBody: func() any { return &gen.DownloadAuthorizationResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := repoCoords(fx.Request.Path)
				_, err := c.CreateDownloadAuthorizationWithResponse(
					ctx, a, r, afterModels(fx.Request.Path), targetID(fx.Request.Path),
					&gen.CreateDownloadAuthorizationParams{})
				return err
			},
		},
		{
			name:         "list_library_models",
			responseBody: func() any { return &gen.PagedLibraryModelItem{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				limit, offset := 20, 0
				_, err := c.ListLibraryModelsWithResponse(ctx, &gen.ListLibraryModelsParams{
					Limit: &limit, Offset: &offset,
				})
				return err
			},
		},
		{
			name:         "get_library_model",
			responseBody: func() any { return &gen.LibraryModelDetailResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				a, r := libraryCoords(fx.Request.Path)
				_, err := c.GetLibraryModelWithResponse(ctx, a, r)
				return err
			},
		},
		{
			name:         "list_library_providers",
			responseBody: func() any { return &gen.ListLibraryProvidersResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				_, err := c.ListLibraryProvidersWithResponse(ctx)
				return err
			},
		},
		{
			name:         "get_usage",
			responseBody: func() any { return &gen.UsageResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				_, err := c.GetUsageWithResponse(ctx)
				return err
			},
		},
		{
			name:         "get_usage_quotas",
			responseBody: func() any { return &gen.UsageQuotasResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				_, err := c.GetUsageQuotasWithResponse(ctx)
				return err
			},
		},
		{
			name:         "get_billing_plan",
			responseBody: func() any { return &gen.BillingPlanResponse{} },
			drive: func(ctx context.Context, c *gen.ClientWithResponses, fx fixture) error {
				_, err := c.GetBillingPlanWithResponse(ctx)
				return err
			},
		},
		// Error envelopes: response-only (the CLI surfaces these through the
		// shared ErrorEnvelope type).
		{
			name:         "error_401",
			responseBody: func() any { return &gen.ErrorEnvelope{} },
		},
		{
			name:         "error_422",
			responseBody: func() any { return &gen.ErrorEnvelope{} },
		},
		{
			// The literal-enum 422 (invalid use_case) — a distinct error shape
			// from the missing-field error_422; both round-trip the shared
			// ErrorEnvelope type with a non-null per-field message.
			name:         "error_422_enum",
			responseBody: func() any { return &gen.ErrorEnvelope{} },
		},
		{
			// An in-progress upload conflict carries the resumable session ID
			// through the same public error envelope.
			name:         "create_model_upload_conflict",
			responseBody: func() any { return &gen.ErrorEnvelope{} },
		},
	}
}

// TestResponseRoundTrip decodes each fixture response body through its
// generated type and asserts a lossless round-trip (dropped/unknown fields
// fail).
func TestResponseRoundTrip(t *testing.T) {
	for _, tc := range cases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fx := loadFixture(t, tc.name)
			if tc.responseBody == nil {
				t.Skip("no response body type wired")
			}
			roundTrip(t, tc.name, fx.Response.Body, tc.responseBody())
		})
	}
}

// TestRequestShape drives each covered operation's generated client call
// through httpmock and asserts the outgoing request matches the fixture.
func TestRequestShape(t *testing.T) {
	for _, tc := range cases() {
		tc := tc
		if tc.drive == nil {
			continue // response-only fixture (error envelopes)
		}
		t.Run(tc.name, func(t *testing.T) {
			fx := loadFixture(t, tc.name)
			got := driveRequest(t, fx, func(ctx context.Context, c *gen.ClientWithResponses) error {
				return tc.drive(ctx, c, fx)
			})
			assertRequest(t, tc.name, fx, got)
		})
	}
}

// TestAllFixturesCovered guards against a new fixture landing in the shared set
// without a corresponding CLI case — otherwise cross-client drift on a new
// operation could slip through unnoticed.
func TestAllFixturesCovered(t *testing.T) {
	dir := fixturesDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading fixtures dir: %v", err)
	}
	covered := map[string]bool{}
	for _, tc := range cases() {
		covered[tc.name] = true
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !covered[name] {
			t.Errorf("fixture %q has no contract case in fixtures_test.go; "+
				"add one so the CLI round-trips it (see cases())", name)
		}
	}
}

// TestFixtureSourceDigest prevents a copied or hand-edited fixture set from
// claiming provenance from a backend commit whose published payloads differ.
func TestFixtureSourceDigest(t *testing.T) {
	dir := fixturesDir(t)
	raw, err := os.ReadFile(filepath.Join(dir, "..", "FIXTURES_SOURCE"))
	if err != nil {
		t.Fatalf("reading fixture source: %v", err)
	}
	var source []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			source = append(source, line)
		}
	}
	if len(source) != 2 {
		t.Fatalf("FIXTURES_SOURCE must contain commit and digest, got %d values", len(source))
	}
	commit, err := hex.DecodeString(source[0])
	if err != nil || len(commit) != 20 {
		t.Fatalf("invalid backend commit in FIXTURES_SOURCE: %q", source[0])
	}
	expected, err := hex.DecodeString(source[1])
	if err != nil || len(expected) != sha256.Size {
		t.Fatalf("invalid fixture digest in FIXTURES_SOURCE: %q", source[1])
	}

	entries, err := os.ReadDir(dir) // sorted by filename
	if err != nil {
		t.Fatalf("reading fixtures: %v", err)
	}
	h := sha256.New()
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("reading fixture %s: %v", entry.Name(), err)
		}
		_, _ = h.Write(payload)
	}
	if !bytes.Equal(h.Sum(nil), expected) {
		t.Fatalf("fixture digest mismatch: got %x, want %s", h.Sum(nil), source[1])
	}
}
