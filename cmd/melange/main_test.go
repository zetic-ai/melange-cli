package main

import (
	"bufio"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmd/root"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

// TestRunHelp is the exit-code integration test: --help must exit 0 through
// the real Run() path.
func TestRunHelp(t *testing.T) {
	code := Run([]string{"--help"})
	assert.Equal(t, 0, code, "--help should exit 0")
}

func TestRunUnknownCommand(t *testing.T) {
	code := Run([]string{"definitely-does-not-exist"})
	assert.Equal(t, 2, code, "unknown command should exit 2")
}

func TestRunVersion(t *testing.T) {
	code := Run([]string{"version"})
	assert.Equal(t, 0, code, "melange version should exit 0")
}

func TestRunNoColor(t *testing.T) {
	code := Run([]string{"--no-color", "version"})
	assert.Equal(t, 0, code, "--no-color should be accepted")
}

// TestRunArgCountErrors pins the exit-code contract for positional-arg
// mistakes: they are usage errors and must exit 2, exactly like flag errors.
// One case per command family (repo, model, auth, api, version).
func TestRunArgCountErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"repo view extra arg", []string{"repo", "view", "a", "b"}},
		{"model status missing arg", []string{"model", "status"}},
		{"auth token extra arg", []string{"auth", "token", "extra"}},
		{"api missing path", []string{"api"}},
		{"version extra arg", []string{"version", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := Run(tt.args)
			assert.Equal(t, 2, code, "arg-count errors must exit 2 (usage error)")
		})
	}
}

// TestRunMCPBadTransport pins the usage-error contract for the mcp command
// through the real Run() path: an unsupported --transport is a flag error and
// must exit 2, never start a server.
func TestRunMCPBadTransport(t *testing.T) {
	code := Run([]string{"mcp", "--transport", "bogus"})
	assert.Equal(t, 2, code, "unsupported --transport must exit 2 (usage error)")
}

// TestRunMCPHelp pins that `melange mcp --help` is a documentation read, not a
// server start: it must exit 0 without ever touching stdin.
func TestRunMCPHelp(t *testing.T) {
	code := Run([]string{"mcp", "--help"})
	assert.Equal(t, 0, code, "melange mcp --help should exit 0")
}

// TestRunMCPHTTPOnlyFlagsWithStdio pins through the real Run() path that a
// flag which only configures the HTTP server is a usage error on stdio. The
// alternative — accepting and ignoring it — would leave an operator believing
// a port is open or that tokens are being validated when neither is true.
func TestRunMCPHTTPOnlyFlagsWithStdio(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"listen", []string{"mcp", "--listen", "127.0.0.1:9"}},
		{"validate-tokens", []string{"mcp", "--validate-tokens"}},
		{"allowed-origins", []string{"mcp", "--allowed-origins", "https://app.zetic.ai"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, 2, Run(tt.args), "http-only flags on stdio must exit 2 (usage error)")
		})
	}
}

// TestRunMCPHTTPTransportSIGINTExitsZero is the exit-code contract for the
// HTTP transport driven through the real entry point, including the real
// signal handler: a running server that receives SIGINT drains and exits 0.
//
// This is the one place the divergence from stdio (where SIGINT exits 130) is
// provable end to end, and it matters operationally: every process supervisor
// that will run this command reads a nonzero status on an orderly stop as a
// crash.
func TestRunMCPHTTPTransportSIGINTExitsZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a process cannot deliver SIGINT to itself on Windows")
	}

	// The server logs the address it bound to stderr, so the listen address
	// can stay :0 and the test never races another process for a fixed port.
	r, w, err := os.Pipe()
	require.NoError(t, err)
	realStderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = realStderr }()

	code := make(chan int, 1)
	go func() { code <- Run([]string{"mcp", "--transport", "http", "--listen", "127.0.0.1:0"}) }()

	addr := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			if m := regexp.MustCompile(`addr=(\S+)`).FindStringSubmatch(scanner.Text()); m != nil {
				addr <- m[1]
				return
			}
		}
	}()

	var listenAddr string
	select {
	case listenAddr = <-addr:
	case got := <-code:
		t.Fatalf("the server exited (code %d) before it logged a listen address", got)
	case <-time.After(30 * time.Second):
		t.Fatal("the server never logged a listen address")
	}

	// Prove it is really serving before signaling: this also guarantees the
	// signal handler is installed, so the SIGINT below can never fall through
	// to the default action and kill the test binary.
	resp, err := http.Get("http://" + listenAddr + "/healthz")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	self, err := os.FindProcess(os.Getpid())
	require.NoError(t, err)
	require.NoError(t, self.Signal(os.Interrupt))

	select {
	case got := <-code:
		assert.Equal(t, 0, got, "SIGINT during an HTTP serve is an orderly stop: exit 0, not 130")
	case <-time.After(30 * time.Second):
		t.Fatal("the server did not exit after SIGINT")
	}
	_ = w.Close()
	_ = r.Close()
}

func TestRunCompletionBash(t *testing.T) {
	code := Run([]string{"completion", "bash"})
	assert.Equal(t, 0, code, "completion bash should exit 0")
}

// TestRunHelpTopicsExitZero pins the help-topic contract through the real
// Run() path: `melange help <topic>` is a successful documentation read and
// must exit 0 for every registered topic (regression: C9 review).
func TestRunHelpTopicsExitZero(t *testing.T) {
	for _, topic := range []string{"environment", "exit-codes", "formatting"} {
		t.Run(topic, func(t *testing.T) {
			code := Run([]string{"help", topic})
			assert.Equal(t, 0, code, "melange help %s must exit 0", topic)
		})
	}
}

// TestHelpContainsExitCodes captures the --help output and asserts it
// documents the exit-code contract (including 130 for interrupted).
func TestHelpContainsExitCodes(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		Executable: "melange",
		Version:    "test",
	}

	cmd := root.NewCmdRoot(f)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs([]string{"--help"})

	require.NoError(t, cmd.Execute(), "--help should not error")

	help := out.String()
	assert.Contains(t, strings.ToLower(help), "exit codes",
		"help output should document the exit-code contract")
	assert.Contains(t, help, "130",
		"help output should mention exit code 130 for interrupted")
}

// TestHelpExamplesAreTruthful pins the root help examples to invocations that
// actually work today: no phantom commands, no missing required flags.
func TestHelpExamplesAreTruthful(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		Executable: "melange",
		Version:    "test",
	}

	cmd := root.NewCmdRoot(f)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs([]string{"--help"})

	require.NoError(t, cmd.Execute(), "--help should not error")

	help := out.String()
	assert.Contains(t, help, "melange repo list --json",
		"help should show a working repo example")
	assert.Contains(t, help, "melange api /v1/me --jq .account.name",
		"help should show a working api example")
	assert.NotContains(t, help, "melange usage",
		"help must not advertise the nonexistent usage command")
	// model upload requires -R ACCOUNT/REPO; the example must include it.
	for _, line := range strings.Split(help, "\n") {
		if strings.Contains(line, "model upload") {
			assert.Contains(t, line, "-R ",
				"model upload example must include the required -R flag")
		}
	}
}
