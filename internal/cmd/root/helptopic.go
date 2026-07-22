package root

import (
	"fmt"

	"github.com/spf13/cobra"
)

// helpTopic is a gh-style additional help topic: a hidden, non-runnable
// command whose only job is to print reference documentation via
// `melange help <topic>` (or `melange <topic> --help`).
type helpTopic struct {
	name  string
	short string
	long  string
}

var helpTopics = []helpTopic{
	{
		name:  "environment",
		short: "Environment variables that melange reads",
		long: `Environment variables that change melange's behavior:

  MELANGE_API_KEY       Personal access token (prefix ztp_) used to
                        authenticate. Takes precedence over every stored
                        credential.
  MELANGE_API_KEY_FILE  Path to a file containing the token. If set but
                        unreadable, commands fail rather than silently
                        falling back to another credential.
  MELANGE_HOST          API host to target (default https://api.zetic.ai).
  MELANGE_API_TIMEOUT   Positive duration bounding each ordinary API call
                        (default 30s; examples: 45s, 2m). Conversion waits
                        and signed storage transfers use separate budgets.
  MELANGE_DEBUG         Set to 1, true, yes, or on (case-insensitive) to
                        log request/response lines to stderr. Headers and
                        tokens are never logged.
  NO_COLOR              Disable color output (any non-empty value).
  TERM                  TERM=dumb disables color output.

Credential precedence (highest wins):

  MELANGE_API_KEY > MELANGE_API_KEY_FILE > OS keyring > config file

Host precedence: --host flag > MELANGE_HOST > config > default.
Run "melange auth status" to see which source the active token came from.

Files:

  Config:       ${XDG_CONFIG_HOME:-~/.config}/melange/config.yml
                (Windows: %AppData%\melange\config.yml)
  Upload state: ${XDG_STATE_HOME:-~/.local/state}/melange/uploads
                (Windows: %LocalAppData%\melange\uploads)
`,
	},
	{
		name:  "exit-codes",
		short: "Exit codes returned by melange",
		long: `melange returns stable exit codes so scripts and agents can branch
on them:

  0    Success
  1    Error: API, network, or command failure
  2    Usage error: bad flags, arguments, or flag combinations
  4    Authentication error: not logged in, or the token was rejected
  130  Interrupted (Ctrl-C); an interrupted upload keeps its session

Agent guidance: treat 2 as a bug in the invocation (fix the command,
do not retry), 4 as a credential problem (re-authenticate, do not
retry), 1 as possibly transient (idempotent requests were already
retried by the CLI), and 130 as cancellation.
`,
	},
	{
		name:  "formatting",
		short: "Structured output with --json, --jq, and --template",
		long: `Structured output flags shared by melange commands:

--json emits the complete documented result as JSON. For server-backed
commands the payload is the API response byte-for-byte: field names,
field order, and unknown fields are preserved exactly. Waited upload and
import commands compose {"model": ..., "status": ...} so agents retain
both the created model key and terminal status. Safety exception: melange
model download --json redacts signed artifact URLs because they are
short-lived credentials.

--jq EXPRESSION filters the result with a jq expression (implies
--json). Filtered values are re-marshaled, so object keys are emitted
in sorted order, not server order. Bare strings print raw, without
quotes.

--template TEMPLATE formats the result with a Go template (implies
--json). Available functions: tablerow (tab-joined row), timeago
(compact relative time), json (marshal a value).

Human output adapts to the terminal: on a TTY, tables print aligned
columns under an uppercase header row; when stdout is not a TTY, rows
are tab-separated values with no header — stable for scripts. Backslash
and control characters inside cells use reversible backslash escapes:
\\, \t, \r, and \n respectively.

List commands emit the page envelope {"results": [...], "count": N}
exactly as the API returned it. --paginate fetches every page and
merges all results arrays into one envelope; every other envelope key
(count, and any keys the server may add) is carried through from the
last page. The merged envelope is re-marshaled, so its top-level keys
are emitted in sorted order (single-page --json output stays byte-exact).
`,
	},
}

// newHelpTopic builds the cobra command for one help topic. The command is
// hidden and not runnable; its help output is exactly the topic text.
func newHelpTopic(topic helpTopic) *cobra.Command {
	cmd := &cobra.Command{
		Use:    topic.name,
		Short:  topic.short,
		Long:   topic.long,
		Hidden: true,
	}
	cmd.SetHelpFunc(func(c *cobra.Command, _ []string) {
		fmt.Fprint(c.OutOrStdout(), c.Long)
	})
	cmd.SetUsageFunc(func(c *cobra.Command) error {
		_, err := fmt.Fprintf(c.ErrOrStderr(), "Usage: melange help %s\n", c.Name())
		return err
	})
	return cmd
}
