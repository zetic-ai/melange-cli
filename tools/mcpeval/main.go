// Command mcpeval is the agent eval harness for the melange MCP server: it
// gives REAL Claude agents Melange jobs to do through the server and scores
// whether they succeed, so a regression in tool descriptions shows up as a
// score delta instead of going unnoticed.
//
// Tasks are YAML files (tools/mcpeval/tasks/*.yaml); each spawns one
// isolated `claude -p` session whose only tools are the melange MCP server's
// catalog, captures the stream-json transcript, and evaluates checks:
// backend state through the melange CLI (`jq_state` — the CLI is the
// independent oracle, never the MCP server itself), transcript assertions
// (`tool_called`/`tool_not_called`), and LLM-scored rubrics (`judge`). The
// product is scorecard.json (machine-diffable) plus a human table.
//
// On-demand only — it needs a live backend, the claude CLI, and Anthropic
// credentials — and never part of the blocking PR gate. Run it via
// `make eval` (script/mcpeval.sh reuses script/mcp-e2e.sh bring-up), or
// directly against an existing backend:
//
//	MELANGE_HOST=... MCPEVAL_PAT_WRITE=ztp_... [MCPEVAL_PAT_READ=ztp_...] \
//	  go run ./tools/mcpeval -melange ./bin/melange
//
// See tools/mcpeval/README.md for the task format, cost, and runtime.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	// Hidden re-exec mode: claude spawns this same binary as the stdio MCP
	// server command, wrapping the real server in the schema shim.
	if len(os.Args) > 1 && os.Args[1] == "-shim-stdio" {
		argv := os.Args[2:]
		if len(argv) > 0 && argv[0] == "--" {
			argv = argv[1:]
		}
		os.Exit(runStdioShim(argv, os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// config is the parsed flag set.
type config struct {
	tasksDir   string
	taskFilter string
	outDir     string
	agentModel string
	judgeModel string
	claudeBin  string
	melangeBin string
	ciSkip     bool
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcpeval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := config{}
	fs.StringVar(&cfg.tasksDir, "tasks", "tools/mcpeval/tasks", "directory of task YAML files")
	fs.StringVar(&cfg.taskFilter, "task", "", "run only these tasks (comma-separated names)")
	fs.StringVar(&cfg.outDir, "out", "mcpeval-out", "output directory (scorecard.json + per-task transcripts)")
	fs.StringVar(&cfg.agentModel, "model", "sonnet", "model for the agent under evaluation")
	fs.StringVar(&cfg.judgeModel, "judge-model", "haiku", "model for judge checks")
	fs.StringVar(&cfg.claudeBin, "claude", envOr("MCPEVAL_CLAUDE", "claude"), "claude CLI binary")
	fs.StringVar(&cfg.melangeBin, "melange", envOr("MCPEVAL_MELANGE", ""), "melange binary (MCP server + CLI oracle); required")
	fs.BoolVar(&cfg.ciSkip, "ci-skip", os.Getenv("MCPEVAL_CI_SKIP") == "1", "exit 0 with a skip message when prerequisites are missing")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	skip := func(reason string) int {
		if cfg.ciSkip {
			fmt.Fprintln(stdout, "MCPEVAL SKIP:", reason)
			return 0
		}
		fmt.Fprintln(stderr, "mcpeval:", reason)
		fmt.Fprintln(stderr, "(pass -ci-skip or set MCPEVAL_CI_SKIP=1 to turn this into a clean skip)")
		return 1
	}

	// ── Preflight: every missing prerequisite is a distinct, actionable
	// message, and in -ci-skip mode a clean exit 0. ──────────────────────
	claudeBin, err := exec.LookPath(cfg.claudeBin)
	if err != nil {
		return skip(fmt.Sprintf("claude CLI not found (%q): agent evals need it — install claude or set MCPEVAL_CLAUDE", cfg.claudeBin))
	}
	claudeVersion, _ := exec.Command(claudeBin, "--version").Output()
	if !claudeAuthed(claudeBin) {
		return skip("no Anthropic credentials: set ANTHROPIC_API_KEY or `claude auth login` first")
	}
	if cfg.melangeBin == "" {
		return skip("no melange binary: pass -melange or set MCPEVAL_MELANGE (script/mcpeval.sh builds one)")
	}
	melangeBin, err := filepath.Abs(cfg.melangeBin)
	if err == nil {
		_, err = os.Stat(melangeBin)
	}
	if err != nil {
		return skip(fmt.Sprintf("melange binary %q: %v", cfg.melangeBin, err))
	}
	host := strings.TrimSuffix(os.Getenv("MELANGE_HOST"), "/")
	patWrite := os.Getenv("MCPEVAL_PAT_WRITE")
	if host == "" || patWrite == "" {
		return skip("no backend: set MELANGE_HOST and MCPEVAL_PAT_WRITE (or run via `make eval`, which brings one up)")
	}

	selfBin, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "mcpeval: resolving own executable:", err)
		return 1
	}
	workDir, err := os.MkdirTemp("", "mcpeval-*")
	if err != nil {
		fmt.Fprintln(stderr, "mcpeval:", err)
		return 1
	}
	defer os.RemoveAll(workDir)
	if err := os.Chmod(workDir, 0o700); err != nil {
		fmt.Fprintln(stderr, "mcpeval:", err)
		return 1
	}

	env := &runEnv{
		host:       host,
		patWrite:   patWrite,
		patRead:    os.Getenv("MCPEVAL_PAT_READ"),
		melangeBin: melangeBin,
		claudeBin:  claudeBin,
		selfBin:    selfBin,
		agentModel: cfg.agentModel,
		judgeModel: cfg.judgeModel,
		workDir:    workDir,
		outDir:     cfg.outDir,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The backend must answer before any tokens are spent; this also
	// resolves the account name every ${ACCOUNT} placeholder expands to.
	account, err := resolveAccount(ctx, env)
	if err != nil {
		return skip(fmt.Sprintf("backend %s did not answer the oracle: %v", host, err))
	}
	env.account = account

	tasks, err := loadTasks(cfg.tasksDir)
	if err != nil {
		fmt.Fprintln(stderr, "mcpeval:", err)
		return 1
	}
	tasks = filterTasks(tasks, cfg.taskFilter)
	if len(tasks) == 0 {
		fmt.Fprintln(stderr, "mcpeval: no tasks match -task", cfg.taskFilter)
		return 1
	}
	vars := map[string]string{
		"ACCOUNT": account,
		"HF_REPO": envOr("MCPEVAL_HF_REPO", "Qwen/Qwen2.5-0.5B-Instruct"),
	}
	for i := range tasks {
		if err := tasks[i].expand(vars); err != nil {
			fmt.Fprintln(stderr, "mcpeval:", err)
			return 1
		}
	}

	if err := os.MkdirAll(cfg.outDir, 0o755); err != nil {
		fmt.Fprintln(stderr, "mcpeval:", err)
		return 1
	}

	run := RunInfo{
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
		MelangeHost:   host,
		Account:       account,
		AgentModel:    cfg.agentModel,
		JudgeModel:    cfg.judgeModel,
		ClaudeVersion: strings.TrimSpace(string(claudeVersion)),
		SchemaShim:    true,
	}
	started := time.Now()
	results := make([]TaskResult, 0, len(tasks))
	for _, task := range tasks {
		fmt.Fprintf(stdout, "==> task %s (%s/%s, max_turns=%d)\n", task.Name, task.Transport, task.Credential, task.MaxTurns)
		res := runTask(ctx, env, task)
		fmt.Fprintf(stdout, "    %s score=%.2f\n", strings.ToUpper(res.Status), res.Score)
		results = append(results, res)
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, "mcpeval: interrupted; scoring what completed")
			break
		}
	}

	card := buildScorecard(run, results, time.Since(started))
	cardPath := filepath.Join(cfg.outDir, "scorecard.json")
	var buf bytes.Buffer
	if err := card.writeJSON(&buf); err != nil {
		fmt.Fprintln(stderr, "mcpeval:", err)
		return 1
	}
	// Scrubbed once more at the boundary: nothing persisted may carry PAT
	// bytes, whatever a check detail or judge reason happened to quote.
	if err := os.WriteFile(cardPath, []byte(env.scrub(buf.String())), 0o644); err != nil {
		fmt.Fprintln(stderr, "mcpeval:", err)
		return 1
	}
	card.writeTable(stdout)
	fmt.Fprintf(stdout, "\nscorecard: %s\n", cardPath)

	// Scores are the product, not the exit code: the run succeeds when every
	// task produced a scored (or deliberately skipped) result. Only a task
	// the harness could not run at all fails the suite.
	for _, r := range results {
		if r.Status == "error" {
			return 1
		}
	}
	if ctx.Err() != nil {
		return 130
	}
	return 0
}

// runTask executes one task end to end: oracle setup, transport bring-up,
// the agent session, checks, oracle cleanup. Every failure path yields a
// TaskResult — a crashing task must never abort the suite.
func runTask(ctx context.Context, env *runEnv, task Task) TaskResult {
	if task.Credential == "read" && env.patRead == "" {
		return skippedTaskResult(task, "no read-only PAT (MCPEVAL_PAT_READ) on this backend")
	}

	for _, step := range task.Setup {
		if _, err := env.oracle(ctx, step.Args); err != nil && !step.MayFail {
			return buildTaskResult(task, Transcript{}, nil,
				fmt.Sprintf("setup `melange %s`: %v", strings.Join(step.Args, " "), err))
		}
	}
	defer func() {
		// Cleanup restores backend state for the next run; failures are
		// non-scoring by design (the state they remove may not exist).
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		defer cancel()
		for _, step := range task.Cleanup {
			_, _ = env.oracle(cleanupCtx, step.Args)
		}
	}()

	serverURL := ""
	if task.Transport == "http" {
		url, stopServer, err := env.startHTTPServer(task)
		if err != nil {
			return buildTaskResult(task, Transcript{}, nil, "starting http transport: "+err.Error())
		}
		defer stopServer()
		serverURL = url
	}
	mcpConfig, err := env.writeMCPConfig(task, serverURL)
	if err != nil {
		return buildTaskResult(task, Transcript{}, nil, "writing mcp config: "+err.Error())
	}

	tr, harnessErr := env.runAgent(ctx, task, mcpConfig)
	checks := evalChecks(ctx, task, tr, env.oracle, env.judge)
	return buildTaskResult(task, tr, checks, harnessErr)
}

// resolveAccount asks the backend (through the oracle CLI) which account the
// write PAT belongs to.
func resolveAccount(ctx context.Context, env *runEnv) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := env.oracle(ctx, []string{"api", "/v1/me"})
	if err != nil {
		return "", err
	}
	var me struct {
		Account struct {
			Name string `json:"name"`
		} `json:"account"`
	}
	if err := json.Unmarshal(out, &me); err != nil {
		return "", fmt.Errorf("parsing /v1/me: %w", err)
	}
	if me.Account.Name == "" {
		return "", fmt.Errorf("/v1/me reported no account name")
	}
	return me.Account.Name, nil
}

// claudeAuthed reports whether the claude CLI has credentials to spend:
// an explicit API key, or a logged-in session.
func claudeAuthed(claudeBin string) bool {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true
	}
	out, err := exec.Command(claudeBin, "auth", "status").Output()
	if err != nil {
		return false
	}
	var status struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		// Older CLIs print human text; treat a mention of a logged-in state
		// as authenticated and let the first real call be the arbiter.
		return strings.Contains(string(out), "Logged in") || strings.Contains(string(out), "loggedIn")
	}
	return status.LoggedIn
}

// filterTasks applies the -task comma-separated name filter.
func filterTasks(tasks []Task, filter string) []Task {
	if filter == "" {
		return tasks
	}
	want := map[string]bool{}
	for _, name := range strings.Split(filter, ",") {
		want[strings.TrimSpace(name)] = true
	}
	out := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if want[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// envOr returns the environment value or a default.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
