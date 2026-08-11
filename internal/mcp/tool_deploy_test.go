package mcp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/edition"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

// Bodies use non-alphabetical key order so any re-marshal through a typed
// struct (which would sort keys) breaks the byte-equality assertions.
const (
	deployOptionsBody = `{"languages":[{"id":"android-kotlin","label":"Android (Kotlin)","code_language":"kotlin"}],` +
		`"inference_modes":[{"id":"auto","label":"Auto","description":"pick per device"}],` +
		`"default_language":"android-kotlin","default_inference_mode":"auto","guide_version":1}`
	deployGuideBody = `{"guide_version":1,"language":"ios-swift","inference_mode":"speed",` +
		`"model":{"repository":"zetic/whisper-tiny","key":"whisper-tiny-1","version":1,` +
		`"type":"general","state":"ready","download_ready":true},` +
		`"credential_placeholder":"YOUR_PERSONAL_KEY","sdk":{"name":"zetic-mlange","version":"1.2.3"},` +
		`"steps":[{"title":"Install","code_language":"swift","code":"import ZeticMLange"}]}`
)

const guidePath = "/v1/repos/zetic/whisper-tiny/models/whisper-tiny-1/deployment-guide"

func TestGetDeploymentInfoWithoutModelKeyReturnsTheOptionsCatalog(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/deployment/options"),
		httpmock.JSONResponse(200, json.RawMessage(deployOptionsBody)))

	cs, wire := connect(t, registryProvider(t, reg))
	// nil arguments marshal to a literal "arguments": null — the shape that
	// crashes the SDK's default-filling, so no schema default may exist.
	res := callTool(t, cs, "get_deployment_info", nil)

	assert.False(t, res.IsError)
	assert.Equal(t, deployOptionsBody, textOf(t, res))

	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+deployOptionsBody,
		"the response bytes cross the wire verbatim")
	reg.Verify(t)
}

func TestGetDeploymentInfoWithModelKeyReturnsTheGuideAndForwardsSelectors(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", guidePath),
		httpmock.JSONResponse(200, json.RawMessage(deployGuideBody)))

	cs, wire := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_deployment_info", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
		"language": "ios-swift", "inference_mode": "speed",
	})

	assert.False(t, res.IsError)
	assert.Equal(t, deployGuideBody, textOf(t, res))

	query := reg.Requests[0].URL.Query()
	assert.Equal(t, "ios-swift", query.Get("language"))
	assert.Equal(t, "speed", query.Get("inference_mode"))

	require.NoError(t, cs.Close())
	assert.Contains(t, wire.String(), `"structuredContent":`+deployGuideBody)
	reg.Verify(t)
}

func TestGetDeploymentInfoOmittedSelectorsLeaveTheServerDefaults(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", guidePath),
		httpmock.JSONResponse(200, json.RawMessage(deployGuideBody)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_deployment_info", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
	})
	assert.False(t, res.IsError)

	query := reg.Requests[0].URL.Query()
	assert.False(t, query.Has("language"),
		"an omitted selector is not sent as an empty filter — the API picks the default")
	assert.False(t, query.Has("inference_mode"))
	reg.Verify(t)
}

func TestGetDeploymentInfoGuideSelectorsWithoutAModelKeyAreRefused(t *testing.T) {
	// Silently returning the catalog would drop the caller's selectors and
	// look like a successful answer to a question never asked.
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"repo alone", map[string]any{"repo": "zetic/whisper-tiny"}},
		{"language alone", map[string]any{"language": "flutter"}},
		{"inference mode alone", map[string]any{"inference_mode": "speed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "get_deployment_info", tc.args)

			assert.True(t, res.IsError)
			assert.Contains(t, textOf(t, res), "model_key is required")
			assert.Empty(t, reg.Requests, "neither endpoint is called for an ambiguous request")
		})
	}
}

func TestGetDeploymentInfoInvalidRepoIsToolErrorWithoutAnAPICall(t *testing.T) {
	reg := &httpmock.Registry{}
	cs, _ := connect(t, registryProvider(t, reg))

	res := callTool(t, cs, "get_deployment_info", map[string]any{
		"repo": "whisper-tiny", "model_key": "whisper-tiny-1",
	})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "ACCOUNT/NAME")
	assert.Empty(t, reg.Requests)
}

func TestGetDeploymentInfoRejectsUnsupportedSelectorsBeforeCallingTheAPI(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))

	// The vocabulary must reach the client, not just the handler: without the
	// advertised enum an unsupported language would travel to the API as a
	// query parameter and come back as an opaque 400.
	schema, err := json.Marshal(toolNamed(t, cs, "get_deployment_info").InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema),
		`"enum":["android-kotlin","android-java","ios-swift","flutter"]`,
		"the supported SDK languages are advertised")
	assert.Contains(t, string(schema), `"enum":["auto","speed","accuracy"]`,
		"the supported inference modes are advertised")
	assert.NotContains(t, string(schema), "react-native",
		"React Native is deliberately unsupported by public-v1")

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"unsupported language", map[string]any{
			"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "language": "react-native"}},
		{"unknown inference mode", map[string]any{
			"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1", "inference_mode": "fastest"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &httpmock.Registry{}
			cs, _ := connect(t, registryProvider(t, reg))

			res := callTool(t, cs, "get_deployment_info", tc.args)

			assert.True(t, res.IsError)
			// The SDK prefixes schema-validation failures this way; without it,
			// an IsError result could just as well be the unmatched-stub
			// transport error, which would pass with no enum at all.
			assert.Contains(t, textOf(t, res), `validating "arguments"`,
				"the selector is rejected by the schema, not by a failed request")
			assert.Empty(t, reg.Requests)
		})
	}
}

func TestQualcommDeploymentToolAdvertisesAndroidAndFlutterButNotIOS(t *testing.T) {
	cs, _ := connectDeps(t, Deps{
		Clients: registryProvider(t, &httpmock.Registry{}), Version: "test", Edition: edition.Qualcomm(),
	})

	schema, err := json.Marshal(toolNamed(t, cs, "get_deployment_info").InputSchema)
	require.NoError(t, err)
	assert.Contains(t, string(schema),
		`"enum":["android-kotlin","android-java","flutter"]`)
	assert.NotContains(t, string(schema), "ios-swift")
}

func TestQualcommDeploymentOptionsFilterIOSFromToolResult(t *testing.T) {
	body := `{"languages":[{"id":"android-kotlin","label":"Android (Kotlin)","code_language":"kotlin"},{"id":"ios-swift","label":"iOS (Swift)","code_language":"swift"},{"id":"flutter","label":"Flutter","code_language":"dart"}],` +
		`"inference_modes":[{"id":"auto","label":"Auto","description":"pick per device"}],"default_language":"android-kotlin","default_inference_mode":"auto","guide_version":1}`
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/deployment/options"),
		httpmock.JSONResponse(200, json.RawMessage(body)))
	cs, _ := connectDeps(t, Deps{Clients: registryProvider(t, reg), Version: "test", Edition: edition.Qualcomm()})

	res := callTool(t, cs, "get_deployment_info", nil)

	assert.False(t, res.IsError)
	assert.Contains(t, textOf(t, res), "flutter")
	assert.NotContains(t, textOf(t, res), "ios-swift")
}

func TestGetDeploymentInfoGuideFailureIsToolError(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", guidePath),
		httpmock.JSONResponse(http.StatusNotFound, json.RawMessage(
			`{"type":"error","error":{"type":"not_found_error","message":"model not found"},"request_id":"req_11"}`)))

	cs, _ := connect(t, registryProvider(t, reg))
	res := callTool(t, cs, "get_deployment_info", map[string]any{
		"repo": "zetic/whisper-tiny", "model_key": "whisper-tiny-1",
	})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "model not found")
	reg.Verify(t)
}

func TestGetDeploymentInfoAdvertisesItsTwoModesAndCredentialSafety(t *testing.T) {
	cs, _ := connect(t, registryProvider(t, &httpmock.Registry{}))
	tool := toolNamed(t, cs, "get_deployment_info")

	// The description is where an agent learns that model_key switches modes,
	// and that the placeholder in the rendered code is not a real credential.
	assert.Contains(t, tool.Description, "model_key")
	assert.Contains(t, tool.Description, "YOUR_PERSONAL_KEY")

	assertReadOnlyAnnotations(t, cs, "get_deployment_info")
}
