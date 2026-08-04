package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// This file exists to compensate for a REAL interop defect that the eval
// harness itself surfaced (see tools/mcpeval/README.md, finding 1): four
// generated tool output schemas (get_deployment_info, get_model,
// get_model_report, search_library) are bare {"anyOf": [...]} unions with no
// top-level "type":"object". The MCP spec types outputSchema as an object
// schema, and Claude Code's client (2.1.220) enforces that literally — its
// tools/list validation fails, it retries, and then drops the ENTIRE
// 18-tool catalog, so a real Claude agent sees zero melange tools.
//
// The catalog is a frozen surface in this task, so the fix (emit
// {"type":"object","anyOf":[...]} from tools/mcpschemas) belongs to the
// controller. Until then the runner interposes this shim between the agent
// client and the server. It rewrites EXACTLY ONE thing: a tools/list result
// whose outputSchema lacks "type" but whose anyOf branches are all
// object-typed gains "type":"object". Descriptions, input schemas, tool
// behavior, and every other message pass through untouched, so eval scores
// still measure the shipped tool descriptions.

// normalizeCatalog rewrites one server->client JSON-RPC message if (and only
// if) it is a tools/list result needing the outputSchema repair. It returns
// the original bytes untouched in every other case, including on parse
// errors — the shim must never break a working stream.
func normalizeCatalog(msg []byte) []byte {
	var envelope map[string]any
	if err := json.Unmarshal(msg, &envelope); err != nil {
		return msg
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		return msg
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		return msg
	}
	changed := false
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		schema, ok := tool["outputSchema"].(map[string]any)
		if !ok {
			continue
		}
		if _, hasType := schema["type"]; hasType {
			continue
		}
		branches, ok := schema["anyOf"].([]any)
		if !ok || len(branches) == 0 {
			continue
		}
		allObjects := true
		for _, b := range branches {
			branch, ok := b.(map[string]any)
			if !ok || branch["type"] != "object" {
				allObjects = false
				break
			}
		}
		if !allObjects {
			continue
		}
		schema["type"] = "object"
		changed = true
	}
	if !changed {
		return msg
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return msg
	}
	return out
}

// runStdioShim spawns the real MCP server command and relays stdio both
// ways, passing server->client lines through normalizeCatalog. The agent
// client (claude) spawns THIS process from its MCP config; argv is the real
// server command line.
func runStdioShim(argv []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "mcpeval -shim-stdio: missing server command after --")
		return 2
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = stderr
	childIn, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintln(stderr, "mcpeval -shim-stdio:", err)
		return 1
	}
	childOut, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(stderr, "mcpeval -shim-stdio:", err)
		return 1
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(stderr, "mcpeval -shim-stdio:", err)
		return 1
	}

	// The MCP client stops a stdio server by closing stdin and/or signaling;
	// forward both so the child always sees the same lifecycle it would
	// without the shim.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for s := range sig {
			_ = cmd.Process.Signal(s)
		}
	}()
	defer signal.Stop(sig)

	go func() {
		_, _ = io.Copy(childIn, stdin)
		_ = childIn.Close()
	}()

	sc := bufio.NewScanner(childOut)
	sc.Buffer(make([]byte, 0, 1024*1024), 32*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if bytes.Contains(line, []byte(`"tools"`)) {
			line = normalizeCatalog(line)
		}
		if _, err := stdout.Write(append(line, '\n')); err != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

// newHTTPShim returns a reverse proxy for the streamable-HTTP transport that
// applies the same tools/list repair to application/json response bodies.
// Everything else — bearer headers, status codes, 401 challenges, SSE
// streams — passes through untouched.
func newHTTPShim(target *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.Header.Get("Content-Type") != "application/json" {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if bytes.Contains(body, []byte(`"tools"`)) {
			body = normalizeCatalog(body)
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", fmt.Sprint(len(body)))
		return nil
	}
	return proxy
}
