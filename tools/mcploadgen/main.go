// Command mcploadgen is the on-demand load harness for `melange mcp
// --transport http`. It drives an initialize + tools/list + tools/call mix
// against a running server and reports RPS, p50/p95/p99 latency, and error
// rate as JSON (stdout) plus a human table (stderr), evaluating p95 budgets
// and exiting nonzero when one is breached. It is NOT run in CI; see
// script/mcp-loadtest.sh and `make perf`.
//
// With --stub-backend it instead runs a tiny stand-in for the Melange API
// (GET /v1/me answers any bearer) so the whole harness needs no network.
//
// Requests are raw stateless-streamable POSTs rather than SDK client calls,
// for measurement fidelity: latency is taken from immediately before the
// request is written to after the response body is fully read, with no SDK
// session queueing in between. Each worker uses its own bearer
// (--token base + worker index) so the server's per-token rate budget
// (120/min, burst 40) maps one bucket per worker; the pre-auth per-IP budget
// (300/min = 5/s sustained from one host) is shared by all workers, which is
// why the default pacing (--rps 4) sits under it — an unpaced localhost soak
// would measure the 429 path, not the server. Throttled responses are counted
// and reported separately so a mis-paced run is visible, not silently folded
// into latency or error numbers.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// config is the parsed flag set.
type config struct {
	target      string
	backend     string
	concurrency int
	duration    time.Duration
	rps         float64
	token       string

	budgetListP95   time.Duration
	budgetReadSlack time.Duration

	stubBackend string
	stubLatency time.Duration
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcploadgen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := config{}
	fs.StringVar(&cfg.target, "target", "http://127.0.0.1:8080", "MCP endpoint URL to load")
	fs.StringVar(&cfg.backend, "backend", "", "backend base URL; when set, its RTT is probed and the read-tool budget becomes RTT + --budget-read-slack")
	fs.IntVar(&cfg.concurrency, "concurrency", 8, "concurrent workers, each with its own bearer")
	fs.DurationVar(&cfg.duration, "duration", 60*time.Second, "soak duration")
	fs.Float64Var(&cfg.rps, "rps", 4, "total request rate across workers; 0 = unpaced (will hit the per-IP limiter from one host)")
	fs.StringVar(&cfg.token, "token", "loadgen-token", "bearer token base; worker i sends <token>-w<i>")
	fs.DurationVar(&cfg.budgetListP95, "budget-list-p95", 50*time.Millisecond, "p95 budget for tools/list")
	fs.DurationVar(&cfg.budgetReadSlack, "budget-read-slack", 20*time.Millisecond, "read-tool p95 budget above backend RTT (needs --backend)")
	fs.StringVar(&cfg.stubBackend, "stub-backend", "", "run as a stub Melange API on this listen address instead of generating load")
	fs.DurationVar(&cfg.stubLatency, "stub-latency", 0, "artificial per-request latency of the stub backend")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if cfg.stubBackend != "" {
		if err := runStub(cfg, stdout); err != nil {
			fmt.Fprintf(stderr, "mcploadgen: %v\n", err)
			return 1
		}
		return 0
	}
	report, err := runLoad(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "mcploadgen: %v\n", err)
		return 1
	}
	printTable(stderr, report)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(stderr, "mcploadgen: encoding report: %v\n", err)
		return 1
	}
	if !report.Pass {
		return 1
	}
	return 0
}

// runStub serves the stand-in Melange API until SIGINT/SIGTERM.
func runStub(cfg config, stdout io.Writer) error {
	ln, err := net.Listen("tcp", cfg.stubBackend)
	if err != nil {
		return fmt.Errorf("binding stub backend %s: %w", cfg.stubBackend, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/me", func(w http.ResponseWriter, r *http.Request) {
		if cfg.stubLatency > 0 {
			time.Sleep(cfg.stubLatency)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"user":{"email":"load@example.com","nickname":"load"},`+
			`"account":{"name":"load","type":"personal"},`+
			`"token":{"name":"load-token","scopes":["read"]}}`)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	// The script greps for this exact line to learn the bound port.
	fmt.Fprintf(stdout, "stub backend listening on http://%s\n", ln.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// op names double as JSON keys and table rows.
const (
	opInitialize = "initialize"
	opToolsList  = "tools/list"
	opToolsCall  = "tools/call"
)

var opBodies = map[string]string{
	opInitialize: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcploadgen","version":"0.0.0"}}}`,
	opToolsList: `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	opToolsCall: `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`,
}

// opStats accumulates one worker's results for one op; merged after the run.
type opStats struct {
	latencies []time.Duration // successful requests only
	errors    int
	throttled int // HTTP 429, excluded from both latency and error counts
}

// OpReport is the per-op section of the JSON report.
type OpReport struct {
	Count     int     `json:"count"`
	Errors    int     `json:"errors"`
	Throttled int     `json:"throttled"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
}

// Budget is one evaluated p95 budget.
type Budget struct {
	Name     string  `json:"name"`
	Op       string  `json:"op"`
	LimitMs  float64 `json:"limit_ms"`
	ActualMs float64 `json:"actual_ms"`
	Pass     bool    `json:"pass"`
}

// Report is the JSON document written to stdout.
type Report struct {
	Target          string              `json:"target"`
	Concurrency     int                 `json:"concurrency"`
	DurationSeconds float64             `json:"duration_seconds"`
	Requests        int                 `json:"requests"`
	RPS             float64             `json:"rps"`
	ErrorRate       float64             `json:"error_rate"`
	BackendRTTMs    *float64            `json:"backend_rtt_ms,omitempty"`
	Ops             map[string]OpReport `json:"ops"`
	Budgets         []Budget            `json:"budgets"`
	Pass            bool                `json:"pass"`
}

func runLoad(cfg config) (*Report, error) {
	if cfg.target == "" {
		return nil, fmt.Errorf("--target is required")
	}
	if cfg.concurrency < 1 {
		return nil, fmt.Errorf("--concurrency must be at least 1")
	}

	// One shared transport sized so every worker keeps a warm connection:
	// latency numbers must measure the server, not client-side re-dials.
	transport := &http.Transport{
		MaxIdleConns:        cfg.concurrency * 2,
		MaxIdleConnsPerHost: cfg.concurrency * 2,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	defer transport.CloseIdleConnections()

	var backendRTT *float64
	if cfg.backend != "" {
		rtt, err := probeBackendRTT(client, cfg.backend)
		if err != nil {
			return nil, fmt.Errorf("probing backend RTT: %w", err)
		}
		ms := float64(rtt) / float64(time.Millisecond)
		backendRTT = &ms
	}

	// Global pacing: one ticker shared by all workers. A ticker's channel
	// holds at most one pending tick, so a stalled run cannot bank a burst.
	var pace <-chan time.Time
	if cfg.rps > 0 {
		ticker := time.NewTicker(time.Duration(float64(time.Second) / cfg.rps))
		defer ticker.Stop()
		pace = ticker.C
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	workers := make([]map[string]*opStats, cfg.concurrency)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < cfg.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workers[i] = runWorker(ctx, client, cfg, fmt.Sprintf("%s-w%02d", cfg.token, i), pace)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	merged := map[string]*opStats{
		opInitialize: {}, opToolsList: {}, opToolsCall: {},
	}
	for _, w := range workers {
		for op, s := range w {
			m := merged[op]
			m.latencies = append(m.latencies, s.latencies...)
			m.errors += s.errors
			m.throttled += s.throttled
		}
	}

	report := &Report{
		Target:          cfg.target,
		Concurrency:     cfg.concurrency,
		DurationSeconds: elapsed.Seconds(),
		BackendRTTMs:    backendRTT,
		Ops:             map[string]OpReport{},
		Pass:            true,
	}
	totalErrors := 0
	for op, s := range merged {
		sort.Slice(s.latencies, func(a, b int) bool { return s.latencies[a] < s.latencies[b] })
		report.Ops[op] = OpReport{
			Count:     len(s.latencies) + s.errors + s.throttled,
			Errors:    s.errors,
			Throttled: s.throttled,
			P50Ms:     ms(percentile(s.latencies, 0.50)),
			P95Ms:     ms(percentile(s.latencies, 0.95)),
			P99Ms:     ms(percentile(s.latencies, 0.99)),
		}
		report.Requests += report.Ops[op].Count
		totalErrors += s.errors
	}
	if report.Requests == 0 {
		return nil, fmt.Errorf("no requests completed; is the target serving?")
	}
	report.RPS = float64(report.Requests) / elapsed.Seconds()
	report.ErrorRate = float64(totalErrors) / float64(report.Requests)

	report.Budgets = append(report.Budgets, evalBudget(
		"tools/list p95", opToolsList, cfg.budgetListP95, merged[opToolsList]))
	if backendRTT != nil {
		limit := time.Duration(*backendRTT*float64(time.Millisecond)) + cfg.budgetReadSlack
		report.Budgets = append(report.Budgets, evalBudget(
			fmt.Sprintf("read tool p95 (backend RTT %.2fms + %s)", *backendRTT, cfg.budgetReadSlack),
			opToolsCall, limit, merged[opToolsCall]))
	}
	for _, b := range report.Budgets {
		if !b.Pass {
			report.Pass = false
		}
	}
	return report, nil
}

// runWorker loops the op mix until ctx expires: one initialize up front (a
// stateless client's reconnect), then alternating tools/list and tools/call.
func runWorker(ctx context.Context, client *http.Client, cfg config, token string, pace <-chan time.Time) map[string]*opStats {
	stats := map[string]*opStats{
		opInitialize: {}, opToolsList: {}, opToolsCall: {},
	}
	for i := 0; ; i++ {
		var op string
		if i == 0 {
			op = opInitialize
		} else if i%2 == 1 {
			op = opToolsList
		} else {
			op = opToolsCall
		}
		if pace != nil {
			select {
			case <-pace:
			case <-ctx.Done():
				return stats
			}
		} else if ctx.Err() != nil {
			return stats
		}
		latency, outcome := doRequest(ctx, client, cfg.target, token, op)
		switch outcome {
		case outcomeOK:
			stats[op].latencies = append(stats[op].latencies, latency)
		case outcomeThrottled:
			stats[op].throttled++
		case outcomeError:
			// Context expiry aborts the in-flight request; that is the end of
			// the soak, not a server failure.
			if ctx.Err() != nil {
				return stats
			}
			stats[op].errors++
		}
	}
}

type outcome int

const (
	outcomeOK outcome = iota
	outcomeThrottled
	outcomeError
)

// doRequest issues one stateless MCP POST and classifies the result. The
// latency spans request write through full response-body read — the number an
// MCP client actually experiences for the call.
func doRequest(ctx context.Context, client *http.Client, target, token, op string) (time.Duration, outcome) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(opBodies[op]))
	if err != nil {
		return 0, outcomeError
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, outcomeError
	}
	body, err := io.ReadAll(resp.Body)
	latency := time.Since(start)
	_ = resp.Body.Close()
	if err != nil {
		return 0, outcomeError
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return 0, outcomeThrottled
	}
	if resp.StatusCode != http.StatusOK {
		return 0, outcomeError
	}
	// A 200 still fails if the JSON-RPC layer errored or the tool reported
	// isError (e.g. the backend rejected the bearer).
	var envelope struct {
		Result *struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil ||
		envelope.Error != nil || envelope.Result == nil || envelope.Result.IsError {
		return 0, outcomeError
	}
	return latency, outcomeOK
}

// probeBackendRTT measures the median of 15 direct GET /v1/me round trips,
// the baseline the read-tool budget is anchored to.
func probeBackendRTT(client *http.Client, backend string) (time.Duration, error) {
	const probes = 15
	samples := make([]time.Duration, 0, probes)
	for i := 0; i < probes; i++ {
		req, err := http.NewRequest(http.MethodGet, strings.TrimRight(backend, "/")+"/v1/me", nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer rtt-probe")
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		_, err = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return 0, err
		}
		samples = append(samples, time.Since(start))
	}
	sort.Slice(samples, func(a, b int) bool { return samples[a] < samples[b] })
	return samples[len(samples)/2], nil
}

// evalBudget checks op's p95 against limit. An op with no successful samples
// fails its budget: a soak that produced nothing cannot demonstrate anything.
func evalBudget(name, op string, limit time.Duration, s *opStats) Budget {
	p95 := percentile(s.latencies, 0.95)
	return Budget{
		Name:     name,
		Op:       op,
		LimitMs:  ms(limit),
		ActualMs: ms(p95),
		Pass:     len(s.latencies) > 0 && p95 < limit,
	}
}

// percentile reads the q-th percentile from an ascending-sorted slice
// (nearest-rank); 0 when empty.
func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q*float64(len(sorted))+0.5) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// printTable renders the human scorecard to stderr.
func printTable(w io.Writer, r *Report) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "op\tcount\terrors\t429\tp50(ms)\tp95(ms)\tp99(ms)\n")
	for _, op := range []string{opInitialize, opToolsList, opToolsCall} {
		o := r.Ops[op]
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%.2f\t%.2f\t%.2f\n",
			op, o.Count, o.Errors, o.Throttled, o.P50Ms, o.P95Ms, o.P99Ms)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\n%d requests in %.1fs = %.1f RPS, error rate %.2f%%\n",
		r.Requests, r.DurationSeconds, r.RPS, 100*r.ErrorRate)
	if r.BackendRTTMs != nil {
		fmt.Fprintf(w, "backend RTT (median of direct probes): %.2fms\n", *r.BackendRTTMs)
	}
	for _, b := range r.Budgets {
		verdict := "PASS"
		if !b.Pass {
			verdict = "FAIL"
		}
		fmt.Fprintf(w, "budget %-40s limit %8.2fms actual %8.2fms  %s\n", b.Name, b.LimitMs, b.ActualMs, verdict)
	}
	if r.Pass {
		fmt.Fprintln(w, "RESULT: PASS")
	} else {
		fmt.Fprintln(w, "RESULT: FAIL")
	}
}
