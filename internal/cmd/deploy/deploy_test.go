package deploy_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/edition"
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

func setup(t *testing.T) *testEnv {
	t.Helper()
	t.Setenv("MELANGE_DEBUG", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("NO_COLOR", "")
	ios, in, out, errOut := iostreams.Test()
	reg := &httpmock.Registry{}
	f := &cmdutil.Factory{IOStreams: ios, Version: "test", HTTPTransport: reg}
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

func jsonStub(status int, body string) httpmock.Responder {
	return httpmock.WithHeader(
		httpmock.StatusStringResponse(status, body),
		"Content-Type", "application/json",
	)
}

const optionsBody = `{"guide_version":1,"default_language":"android-kotlin","default_inference_mode":"auto","languages":[{"id":"android-kotlin","label":"Android (Kotlin)","code_language":"kotlin"},{"id":"ios-swift","label":"iOS (Swift)","code_language":"swift"}],"inference_modes":[{"id":"auto","label":"Auto","description":"Balanced"},{"id":"speed","label":"Speed","description":"Fast"}]}`

const guideBody = `{"guide_version":1,"language":"ios-swift","inference_mode":"speed","model":{"repository":"acme/chat","key":"abc123","version":7,"type":"llm","state":"ready","download_ready":true},"sdk":{"name":"ZeticMLange","version":"1.9.0"},"credential_placeholder":"YOUR_PERSONAL_KEY","steps":[{"title":"Add package","code_language":"swift","code":"package line"},{"title":"Load and run","code_language":"swift","code":"let model = try await ZeticMLangeLLMModel()"}]}`

func TestDeployOptionsJSONIsExact(t *testing.T) {
	e := setup(t)
	e.reg.Register(httpmock.REST("GET", "/v1/deployment/options"), jsonStub(200, optionsBody))

	require.NoError(t, run(t, e, "deploy", "options", "--json"))
	assert.Equal(t, optionsBody+"\n", e.out.String())
}

func TestDeployOptionsHumanShowsDefaultsAndAllSelectors(t *testing.T) {
	e := setup(t)
	e.f.IOStreams.SetStdoutTTY(true)
	e.reg.Register(httpmock.REST("GET", "/v1/deployment/options"), jsonStub(200, optionsBody))

	require.NoError(t, run(t, e, "deploy", "options"))
	out := e.out.String()
	assert.Contains(t, out, "Android (Kotlin)")
	assert.Contains(t, out, "iOS (Swift)")
	assert.Contains(t, out, "Auto")
	assert.Contains(t, out, "Speed")
	assert.Contains(t, out, "default")
}

func TestQualcommDeployOptionsHideIOSAndKeepFlutter(t *testing.T) {
	e := setup(t)
	e.f.Edition = edition.Qualcomm()
	body := strings.Replace(optionsBody,
		`{"id":"ios-swift","label":"iOS (Swift)","code_language":"swift"}`,
		`{"id":"ios-swift","label":"iOS (Swift)","code_language":"swift"},{"id":"flutter","label":"Flutter","code_language":"dart"}`, 1)
	e.reg.Register(httpmock.REST("GET", "/v1/deployment/options"), jsonStub(200, body))

	require.NoError(t, run(t, e, "deploy", "options", "--json"))
	assert.Contains(t, e.out.String(), `"id":"android-kotlin"`)
	assert.Contains(t, e.out.String(), `"id":"flutter"`)
	assert.NotContains(t, e.out.String(), "ios-swift")
}

func TestQualcommDeployGuideRejectsIOSLocally(t *testing.T) {
	e := setup(t)
	e.f.Edition = edition.Qualcomm()

	err := run(t, e, "deploy", "guide", "abc123", "-R", "acme/chat", "--language", "ios-swift")

	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "android-kotlin, android-java, or flutter")
	assert.Empty(t, e.reg.Requests)
}

func TestQualcommDeployGuideHelpOnlyOffersApprovedLanguages(t *testing.T) {
	e := setup(t)
	e.f.Edition = edition.Qualcomm()

	require.NoError(t, run(t, e, "deploy", "guide", "--help"))
	assert.Contains(t, e.out.String(), "android-kotlin, android-java, or flutter")
	assert.NotContains(t, e.out.String(), "ios-swift")
}

func TestQualcommDeployGuideAllowsAndroidAndFlutter(t *testing.T) {
	for _, language := range []string{"android-kotlin", "android-java", "flutter"} {
		t.Run(language, func(t *testing.T) {
			e := setup(t)
			e.f.Edition = edition.Qualcomm()
			body := strings.ReplaceAll(guideBody, "ios-swift", language)
			e.reg.Register(
				httpmock.REST("GET", "/v1/repos/acme/chat/models/abc123/deployment-guide"),
				jsonStub(200, body),
			)

			require.NoError(t, run(t, e, "deploy", "guide", "abc123", "-R", "acme/chat", "--language", language, "--json"))
			assert.Equal(t, language, e.reg.Requests[0].URL.Query().Get("language"))
			assert.Equal(t, body+"\n", e.out.String())
		})
	}
}

func TestQualcommDeployGuideRejectsUnsupportedServerResponse(t *testing.T) {
	e := setup(t)
	e.f.Edition = edition.Qualcomm()
	e.reg.Register(
		httpmock.REST("GET", "/v1/repos/acme/chat/models/abc123/deployment-guide"),
		jsonStub(200, guideBody),
	)

	err := run(t, e, "deploy", "guide", "abc123", "-R", "acme/chat", "--language", "android-kotlin", "--json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported language")
	assert.Empty(t, e.out.String())
}

func TestDeployGuideSendsExactSelectorsAndPrintsCopyableCodeWhenCaptured(t *testing.T) {
	e := setup(t)
	e.reg.Register(
		httpmock.REST("GET", "/v1/repos/acme/chat/models/abc123/deployment-guide"),
		jsonStub(200, guideBody),
	)

	require.NoError(t, run(t, e, "deploy", "guide", "abc123", "-R", "acme/chat", "--language", "ios-swift", "--mode", "speed"))
	require.Len(t, e.reg.Requests, 1)
	query := e.reg.Requests[0].URL.Query()
	assert.Equal(t, "ios-swift", query.Get("language"))
	assert.Equal(t, "speed", query.Get("inference_mode"))
	out := e.out.String()
	assert.Contains(t, out, "acme/chat")
	assert.Contains(t, out, "abc123")
	assert.Contains(t, out, "YOUR_PERSONAL_KEY")
	assert.Contains(t, out, "```swift\npackage line\n```")
	assert.Contains(t, out, "let model = try await ZeticMLangeLLMModel()")
}

func TestDeployGuideDefaultsMatchDashboard(t *testing.T) {
	e := setup(t)
	e.reg.Register(
		httpmock.REST("GET", "/v1/repos/acme/chat/models/abc123/deployment-guide"),
		jsonStub(200, guideBody),
	)

	require.NoError(t, run(t, e, "deploy", "guide", "abc123", "-R", "acme/chat", "--json"))
	query := e.reg.Requests[0].URL.Query()
	assert.Equal(t, "android-kotlin", query.Get("language"))
	assert.Equal(t, "auto", query.Get("inference_mode"))
	assert.Equal(t, guideBody+"\n", e.out.String())
}

func TestDeployGuideRejectsInvalidSelectorsLocally(t *testing.T) {
	for _, args := range [][]string{
		{"deploy", "guide", "abc123", "-R", "acme/chat", "--language", "react-native"},
		{"deploy", "guide", "abc123", "-R", "acme/chat", "--mode", "turbo"},
		{"deploy", "guide", "abc123", "-R", "bad"},
	} {
		e := setup(t)
		err := run(t, e, args...)
		require.Error(t, err)
		assert.Equal(t, 2, cmdutil.ExitCode(err))
		assert.Empty(t, e.reg.Requests)
		assert.Empty(t, e.out.String())
	}
}

func TestDeployGuideNeverOffersTokenInterpolation(t *testing.T) {
	e := setup(t)
	require.NoError(t, run(t, e, "deploy", "guide", "--help"))
	help := e.out.String()
	assert.Contains(t, help, "YOUR_PERSONAL_KEY")
	assert.NotContains(t, help, "unsafe")
	assert.NotContains(t, help, "current token")
}

func TestDeployGuideRejectsUnexpectedCredentialPlaceholderBeforeAnyRendering(t *testing.T) {
	for _, structured := range []bool{false, true} {
		name := "human"
		if structured {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			body := strings.Replace(guideBody, "YOUR_PERSONAL_KEY", "ztp_server_secret", 1)
			e.reg.Register(
				httpmock.REST("GET", "/v1/repos/acme/chat/models/abc123/deployment-guide"),
				jsonStub(200, body),
			)
			args := []string{"deploy", "guide", "abc123", "-R", "acme/chat"}
			if structured {
				args = append(args, "--json")
			}

			err := run(t, e, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "credential_placeholder")
			assert.NotContains(t, err.Error(), "ztp_server_secret")
			assert.Empty(t, e.out.String(), "an invalid guide must not be rendered in any mode")
		})
	}
}

func TestDeployGuideHumanOutputNeutralizesOSC52ButJSONStaysExact(t *testing.T) {
	body := strings.Replace(guideBody, "package line",
		`safe\u001b]52;c;Y2xpcGJvYXJkLXNlY3JldA==\u0007 text`, 1)

	human := setup(t)
	human.reg.Register(
		httpmock.REST("GET", "/v1/repos/acme/chat/models/abc123/deployment-guide"),
		jsonStub(200, body),
	)
	require.NoError(t, run(t, human, "deploy", "guide", "abc123", "-R", "acme/chat"))
	assert.Contains(t, human.out.String(), "safe text")
	assert.NotContains(t, human.out.String(), "\x1b")
	assert.NotContains(t, human.out.String(), "Y2xpcGJvYXJk")

	structured := setup(t)
	structured.reg.Register(
		httpmock.REST("GET", "/v1/repos/acme/chat/models/abc123/deployment-guide"),
		jsonStub(200, body),
	)
	require.NoError(t, run(t, structured, "deploy", "guide", "abc123", "-R", "acme/chat", "--json"))
	assert.Equal(t, body+"\n", structured.out.String(),
		"terminal sanitization must not mutate structured JSON")
}
