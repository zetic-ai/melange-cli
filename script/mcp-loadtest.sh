#!/usr/bin/env bash
# mcp-loadtest.sh — on-demand soak of `melange mcp --transport http`.
#
# Spins up the mcploadgen stub backend and a real melange HTTP server against
# it, runs a soak (default 60s), prints the scorecard, and exits nonzero when a
# p95 budget is breached (tools/list p95 < 50ms; read tool p95 < backend RTT +
# 20ms — see tools/mcploadgen flags). RSS is sampled at soak start and end as
# a soft memory-flatline report: growth prints a WARNING but never fails the
# run (the hard gate belongs to CLI-PR5's nightly).
#
# NOT wired into ci.yml; run via `make perf`. Tunables (env):
#   DURATION (default 60s), CONCURRENCY (default 8), RPS (default 4),
#   LIST_BUDGET (default 50ms), READ_SLACK (default 20ms)
set -euo pipefail

DURATION="${DURATION:-60s}"
CONCURRENCY="${CONCURRENCY:-8}"
RPS="${RPS:-4}"
LIST_BUDGET="${LIST_BUDGET:-50ms}"
READ_SLACK="${READ_SLACK:-20ms}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
WORK="$(mktemp -d)"
STUB_PID=""
SERVER_PID=""

cleanup() {
    [ -n "$SERVER_PID" ] && kill -INT "$SERVER_PID" 2>/dev/null || true
    [ -n "$STUB_PID" ] && kill -INT "$STUB_PID" 2>/dev/null || true
    [ -n "$SERVER_PID" ] && wait "$SERVER_PID" 2>/dev/null || true
    [ -n "$STUB_PID" ] && wait "$STUB_PID" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

echo "==> building melange and mcploadgen"
go build -o "$WORK/melange" ./cmd/melange
go build -o "$WORK/mcploadgen" ./tools/mcploadgen

echo "==> starting stub backend"
"$WORK/mcploadgen" --stub-backend 127.0.0.1:0 >"$WORK/stub.log" 2>&1 &
STUB_PID=$!
STUB_URL=""
for _ in $(seq 1 50); do
    STUB_URL="$(grep -m1 -o 'http://[0-9.]*:[0-9]*' "$WORK/stub.log" 2>/dev/null || true)"
    [ -n "$STUB_URL" ] && break
    sleep 0.1
done
[ -n "$STUB_URL" ] || { echo "FATAL: stub backend never reported its address"; cat "$WORK/stub.log"; exit 1; }
echo "    stub backend: $STUB_URL"

echo "==> starting melange mcp --transport http"
"$WORK/melange" mcp --transport http --listen 127.0.0.1:0 --host "$STUB_URL" \
    2>"$WORK/server.log" &
SERVER_PID=$!
MCP_ADDR=""
for _ in $(seq 1 50); do
    MCP_ADDR="$(grep -m1 -o 'addr=[0-9.]*:[0-9]*' "$WORK/server.log" 2>/dev/null | cut -d= -f2 || true)"
    if [ -n "$MCP_ADDR" ] && curl -fsS "http://$MCP_ADDR/healthz" >/dev/null 2>&1; then
        break
    fi
    MCP_ADDR=""
    sleep 0.1
done
[ -n "$MCP_ADDR" ] || { echo "FATAL: melange mcp never became healthy"; cat "$WORK/server.log"; exit 1; }
echo "    MCP server: http://$MCP_ADDR (pid $SERVER_PID)"

rss_kb() { ps -o rss= -p "$1" | tr -d ' '; }

# Warm up before the first RSS sample: a Go process sizes its heap during the
# first seconds of traffic, so sampling a cold server start would report that
# one-time warmup growth as a "leak" on every run. Steady-state to
# steady-state is the comparison that means something.
echo "==> warmup (5s)"
"$WORK/mcploadgen" --target "http://$MCP_ADDR" --duration 5s --rps "$RPS" \
    --concurrency "$CONCURRENCY" >/dev/null 2>&1 || true
RSS_START="$(rss_kb "$SERVER_PID")"

echo "==> soaking for $DURATION (concurrency $CONCURRENCY, $RPS rps)"
STATUS=0
"$WORK/mcploadgen" \
    --target "http://$MCP_ADDR" \
    --backend "$STUB_URL" \
    --concurrency "$CONCURRENCY" \
    --duration "$DURATION" \
    --rps "$RPS" \
    --budget-list-p95 "$LIST_BUDGET" \
    --budget-read-slack "$READ_SLACK" \
    >"$WORK/results.json" || STATUS=$?

RSS_END="$(rss_kb "$SERVER_PID")"

echo ""
echo "==> results JSON"
cat "$WORK/results.json"

echo ""
echo "==> memory flatline (soft report)"
echo "    server RSS: start ${RSS_START}KB, end ${RSS_END}KB"
if [ -n "$RSS_START" ] && [ -n "$RSS_END" ]; then
    GROWTH=$((RSS_END - RSS_START))
    # Soft threshold: >25% and >10MB growth over the soak smells like a leak.
    if [ "$GROWTH" -gt 10240 ] && [ "$GROWTH" -gt $((RSS_START / 4)) ]; then
        echo "    WARNING: RSS grew ${GROWTH}KB during the soak — investigate before shipping"
    else
        echo "    RSS flat within tolerance (delta ${GROWTH}KB)"
    fi
fi

exit "$STATUS"
