package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// runEnv is everything a task run needs from the outside world.
type runEnv struct {
	host       string // Melange backend base URL
	account    string // account name behind the write PAT
	patWrite   string // write-scoped PAT: the agent's stdio credential and the oracle's
	patRead    string // read-only PAT for credential: read tasks ("" skips them)
	melangeBin string // built melange binary (MCP server + CLI oracle)
	claudeBin  string // claude CLI
	agentModel string
	judgeModel string
	workDir    string // per-run scratch (0700); holds configs, transcripts, cwds
	outDir     string // persisted artifacts: scorecard + per-task transcripts
}

// scrub replaces credential bytes in s. Defense in depth: the server already
// redacts secrets, but nothing the harness persists may depend on that.
func (e *runEnv) scrub(s string) string {
	for _, secret := range []string{e.patWrite, e.patRead} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "<pat:redacted>")
		}
	}
	return s
}

// oracle runs `melange <args>` as the independent state oracle, with the
// write PAT and --no-input so nothing ever prompts.
func (e *runEnv) oracle(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.melangeBin, append(args, "--no-input")...)
	cmd.Env = append(os.Environ(), "MELANGE_HOST="+e.host, "MELANGE_API_KEY="+e.patWrite)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, e.scrub(strings.TrimSpace(stderr.String())))
	}
	return stdout.Bytes(), nil
}

// writeMCPConfig produces the per-task MCP config file claude loads. The
// stdio transport spawns `melange mcp` directly; the HTTP transport points
// at a runner-managed `melange mcp --transport http` (serverURL).
func (e *runEnv) writeMCPConfig(task Task, serverURL string) (string, error) {
	var server map[string]any
	if task.Transport == "http" {
		pat := e.patRead
		if task.Credential == "write" {
			pat = e.patWrite
		}
		server = map[string]any{
			"type":    "http",
			"url":     serverURL,
			"headers": map[string]string{"Authorization": "Bearer " + pat},
		}
	} else {
		server = map[string]any{
			"command": e.melangeBin,
			"args":    []string{"mcp"},
			"env": map[string]string{
				"MELANGE_HOST":    e.host,
				"MELANGE_API_KEY": e.patWrite,
			},
		}
	}
	cfg := map[string]any{"mcpServers": map[string]any{"melange": server}}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	path := filepath.Join(e.workDir, task.Name+".mcp.json")
	return path, os.WriteFile(path, data, 0o600)
}

// addrRe scrapes the listen address the HTTP server logs on startup.
var addrRe = regexp.MustCompile(`addr=([0-9.]+:[0-9]+)`)

// startHTTPServer runs `melange mcp --transport http --validate-tokens` on
// an ephemeral port and returns the URL the agent should connect to and a
// stop func. --validate-tokens matters: it makes the server resolve each
// bearer's granted scopes via /v1/me, which is what arms scope enforcement
// for the scope-refusal task.
func (e *runEnv) startHTTPServer(task Task) (string, func(), error) {
	logPath := filepath.Join(e.workDir, task.Name+".httpserver.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", nil, err
	}
	cmd := exec.Command(e.melangeBin, "mcp", "--transport", "http",
		"--listen", "127.0.0.1:0", "--validate-tokens", "--host", e.host)
	cmd.Stderr = logFile
	cmd.Stdout = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return "", nil, err
	}
	stopServer := func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		logFile.Close()
	}

	var addr string
	for i := 0; i < 100; i++ {
		data, _ := os.ReadFile(logPath)
		if m := addrRe.FindSubmatch(data); m != nil {
			addr = string(m[1])
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if addr == "" {
		stopServer()
		return "", nil, fmt.Errorf("melange mcp --transport http never logged its address (see %s)", logPath)
	}

	return "http://" + addr + "/", stopServer, nil
}

// runAgent spawns one claude session for the task and returns its parsed
// transcript. The raw stream and stderr are persisted under outDir for
// diagnosis. A timeout or crash is reported in harnessErr, never as a Go
// error: the caller still evaluates checks against the partial transcript.
func (e *runEnv) runAgent(ctx context.Context, task Task, mcpConfig string) (tr Transcript, harnessErr string) {
	taskDir := filepath.Join(e.outDir, task.Name)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return tr, "creating task dir: " + err.Error()
	}
	cwd := filepath.Join(e.workDir, task.Name+".cwd")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return tr, "creating agent cwd: " + err.Error()
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(task.TimeoutSeconds)*time.Second)
	defer cancel()

	// The isolation flags make the run reproducible: no user/project
	// settings, no CLAUDE.md, no skills, no built-in tools (the melange MCP
	// server is the agent's entire world), and only the task's MCP config.
	args := []string{
		"-p", task.Prompt,
		"--model", e.agentModel,
		"--mcp-config", mcpConfig,
		"--strict-mcp-config",
		"--setting-sources", "",
		"--disable-slash-commands",
		"--tools", "",
		"--allowedTools", "mcp__melange",
		"--max-turns", fmt.Sprint(task.MaxTurns),
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
	}
	cmd := exec.CommandContext(ctx, e.claudeBin, args...)
	cmd.Dir = cwd
	cmd.WaitDelay = 10 * time.Second
	// ENABLE_TOOL_SEARCH=0 is load-bearing: with tool search on (the
	// default), Claude Code defers MCP tools behind its ToolSearch built-in —
	// which the isolation flags disable — so the agent would see ZERO melange
	// tools. Forcing it off inlines the whole catalog into the session.
	cmd.Env = append(os.Environ(), "ENABLE_TOOL_SEARCH=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	// Persist scrubbed raw streams for diagnosis before any early return.
	_ = os.WriteFile(filepath.Join(taskDir, "transcript.ndjson"),
		[]byte(e.scrub(stdout.String())), 0o644)
	if stderr.Len() > 0 {
		_ = os.WriteFile(filepath.Join(taskDir, "stderr.log"),
			[]byte(e.scrub(stderr.String())), 0o644)
	}

	tr = parseTranscript(bytes.NewReader(stdout.Bytes()))
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		harnessErr = fmt.Sprintf("task timeout after %ds (transcript may be partial)", task.TimeoutSeconds)
	case runErr != nil:
		harnessErr = e.scrub(fmt.Sprintf("claude exited: %v: %s", runErr, excerpt(stderr.String(), 300)))
	}
	return tr, harnessErr
}

// judge scores a rubric with one non-agentic LLM call through the same
// claude CLI (structured output via --json-schema).
func (e *runEnv) judge(ctx context.Context, task Task, rubric string, tr Transcript) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	schema := `{"type":"object","properties":{"pass":{"type":"boolean"},"reason":{"type":"string"}},"required":["pass","reason"]}`
	args := []string{
		"-p", buildJudgePrompt(task, rubric, tr),
		"--model", e.judgeModel,
		"--strict-mcp-config",
		"--setting-sources", "",
		"--disable-slash-commands",
		"--tools", "",
		"--max-turns", "1",
		"--output-format", "json",
		"--no-session-persistence",
		"--json-schema", schema,
	}
	cmd := exec.CommandContext(ctx, e.claudeBin, args...)
	cmd.Dir = e.workDir
	cmd.WaitDelay = 10 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, "", fmt.Errorf("%w: %s", err, e.scrub(excerpt(stderr.String(), 200)))
	}
	return parseJudgeVerdict(stdout.Bytes())
}

// buildJudgePrompt renders the grading request: the rubric plus everything
// the judge may consider (task prompt, tool sequence, final answer).
func buildJudgePrompt(task Task, rubric string, tr Transcript) string {
	var b strings.Builder
	b.WriteString("You are grading an AI agent's transcript against a rubric. " +
		"Judge ONLY by the rubric; ignore style.\n\n## Task the agent was given\n")
	b.WriteString(task.Prompt)
	b.WriteString("\n\n## Tool calls the agent made, in order\n")
	if len(tr.ToolCalls) == 0 {
		b.WriteString("(none)\n")
	}
	for i, call := range tr.ToolCalls {
		status := "ok"
		if call.IsError {
			status = "error"
		}
		fmt.Fprintf(&b, "%d. %s(%s) -> %s: %s\n", i+1, call.Name,
			excerpt(string(call.Input), 300), status, excerpt(call.Result, 300))
	}
	b.WriteString("\n## Agent's final answer\n")
	if tr.FinalAnswer == "" {
		b.WriteString("(no final answer was produced)\n")
	} else {
		b.WriteString(tr.FinalAnswer)
	}
	b.WriteString("\n\n## Rubric\n")
	b.WriteString(rubric)
	b.WriteString("\nReturn {\"pass\": true|false, \"reason\": \"...\"} strictly per the rubric.")
	return b.String()
}

// parseJudgeVerdict extracts the {pass, reason} verdict from a
// `claude --output-format json --json-schema` response, whose .result field
// carries the schema-constrained JSON as text.
func parseJudgeVerdict(out []byte) (bool, string, error) {
	var envelope struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return false, "", fmt.Errorf("judge output is not JSON: %w", err)
	}
	if envelope.IsError {
		return false, "", fmt.Errorf("judge call errored (%s)", envelope.Subtype)
	}
	text := strings.TrimSpace(envelope.Result)
	// Tolerate a fenced or prefixed verdict: take the outermost JSON object.
	if i := strings.Index(text, "{"); i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[i : j+1]
		}
	}
	var verdict struct {
		Pass   bool   `json:"pass"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(text), &verdict); err != nil {
		return false, "", fmt.Errorf("judge verdict is not the expected shape: %w (got %q)", err, excerpt(envelope.Result, 200))
	}
	return verdict.Pass, verdict.Reason, nil
}
