package cmdutil_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

const pageBody = `{"results":[{"full_name":"zetic/whisper-tiny","count_of_likes":2},` +
	`{"full_name":"acme/detr"}],"count":2}`

// runJSONFlags executes a stub command with AddJSONFlags wired and returns
// the exporter selected by flag parsing.
func runJSONFlags(t *testing.T, args ...string) (*cmdutil.Exporter, error) {
	t.Helper()
	var exporter *cmdutil.Exporter
	cmd := &cobra.Command{Use: "stub", RunE: func(*cobra.Command, []string) error { return nil }}
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmdutil.AddJSONFlags(cmd, &exporter)
	_, err := cmd.ExecuteC()
	return exporter, err
}

func TestJSONFlagsNoneGivenLeavesExporterNil(t *testing.T) {
	exporter, err := runJSONFlags(t)
	require.NoError(t, err)
	assert.Nil(t, exporter)
}

func TestJSONFlagsBareJSONPassthrough(t *testing.T) {
	exporter, err := runJSONFlags(t, "--json")
	require.NoError(t, err)
	require.NotNil(t, exporter)

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, exporter.Write(ios, json.RawMessage(pageBody)))
	assert.Equal(t, pageBody+"\n", out.String(), "the envelope must pass through byte-exact")
}

func TestJSONFlagsNormalizesExistingTrailingJSONWhitespace(t *testing.T) {
	exporter, err := runJSONFlags(t, "--json")
	require.NoError(t, err)

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, exporter.Write(ios, json.RawMessage(pageBody+"\n \t\r\n")))
	assert.Equal(t, pageBody+"\n", out.String(), "JSON output has exactly one trailing newline")
}

func TestJSONFlagsMarshalsNonRawData(t *testing.T) {
	exporter, err := runJSONFlags(t, "--json")
	require.NoError(t, err)

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, exporter.Write(ios, map[string]any{"count": 2}))
	assert.Equal(t, "{\"count\":2}\n", out.String())
}

func TestJSONFlagsJQImpliesJSON(t *testing.T) {
	exporter, err := runJSONFlags(t, "--jq", ".count")
	require.NoError(t, err)
	require.NotNil(t, exporter, "--jq must imply --json")

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, exporter.Write(ios, json.RawMessage(pageBody)))
	assert.Equal(t, "2\n", out.String())
}

func TestJSONFlagsJQIteratesResults(t *testing.T) {
	exporter, err := runJSONFlags(t, "--jq", ".results[].full_name")
	require.NoError(t, err)

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, exporter.Write(ios, json.RawMessage(pageBody)))
	assert.Equal(t, "zetic/whisper-tiny\nacme/detr\n", out.String(),
		"strings must print raw, one result per line")
}

func TestJSONFlagsJQNonStringResultsPrintAsJSON(t *testing.T) {
	exporter, err := runJSONFlags(t, "--jq", ".results[0]")
	require.NoError(t, err)

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, exporter.Write(ios, json.RawMessage(pageBody)))
	assert.Equal(t, "{\"count_of_likes\":2,\"full_name\":\"zetic/whisper-tiny\"}\n", out.String())
}

func TestJSONFlagsJQSyntaxErrorIsFlagError(t *testing.T) {
	_, err := runJSONFlags(t, "--jq", ".results[")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err), "a bad --jq expression is a usage error")
}

func TestJSONFlagsJQRuntimeErrorIsPlainExit1(t *testing.T) {
	exporter, err := runJSONFlags(t, "--jq", `.count + "x"`)
	require.NoError(t, err, "the expression is syntactically valid, so parsing succeeds")
	require.NotNil(t, exporter)

	ios, _, _, _ := iostreams.Test()
	werr := exporter.Write(ios, json.RawMessage(pageBody))
	require.Error(t, werr)
	assert.Equal(t, 1, cmdutil.ExitCode(werr),
		"a jq evaluation failure is a runtime error (exit 1), not a usage error")
}

func TestJSONFlagsTemplateTablerow(t *testing.T) {
	exporter, err := runJSONFlags(t, "--template",
		`{{range .results}}{{tablerow .full_name .count_of_likes}}{{end}}`)
	require.NoError(t, err)
	require.NotNil(t, exporter, "--template must imply the structured path")

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, exporter.Write(ios, json.RawMessage(pageBody)))
	assert.Equal(t, "zetic/whisper-tiny\t2\nacme/detr\t<nil>\n", out.String())
}

func TestJSONFlagsTemplateJSONFunc(t *testing.T) {
	exporter, err := runJSONFlags(t, "--template", `{{.count | json}}`)
	require.NoError(t, err)

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, exporter.Write(ios, json.RawMessage(pageBody)))
	assert.Equal(t, "2", out.String())
}

func TestJSONFlagsTemplateTimeagoFunc(t *testing.T) {
	exporter, err := runJSONFlags(t, "--template", `{{timeago .updated_at}}`)
	require.NoError(t, err)

	body, merr := json.Marshal(map[string]string{
		"updated_at": time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339),
	})
	require.NoError(t, merr)

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, exporter.Write(ios, json.RawMessage(body)))
	assert.Equal(t, "3h ago", out.String())
}

func TestJSONFlagsTemplateSyntaxErrorIsFlagError(t *testing.T) {
	_, err := runJSONFlags(t, "--template", "{{.count")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
}

func TestJSONFlagsJQAndTemplateConflict(t *testing.T) {
	_, err := runJSONFlags(t, "--jq", ".count", "--template", "{{.count}}")
	require.Error(t, err)
	assert.Equal(t, 2, cmdutil.ExitCode(err))
	assert.Contains(t, err.Error(), "--jq")
	assert.Contains(t, err.Error(), "--template")
}

// ---------------------------------------------------------------------------
// NewExporter (direct construction, used by `melange api`)
// ---------------------------------------------------------------------------

func TestNewExporterJQ(t *testing.T) {
	e, err := cmdutil.NewExporter(".results[].full_name", "")
	require.NoError(t, err)

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, e.Write(ios, json.RawMessage(pageBody)))
	assert.Equal(t, "zetic/whisper-tiny\nacme/detr\n", out.String())
}

func TestNewExporterTemplate(t *testing.T) {
	e, err := cmdutil.NewExporter("", "{{.count}}")
	require.NoError(t, err)

	ios, _, out, _ := iostreams.Test()
	require.NoError(t, e.Write(ios, json.RawMessage(pageBody)))
	assert.Equal(t, "2", out.String())
}

func TestNewExporterInvalidExpressions(t *testing.T) {
	_, err := cmdutil.NewExporter(".foo[", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --jq expression")

	_, err = cmdutil.NewExporter("", "{{.count")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --template")
}
