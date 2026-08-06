#!/usr/bin/env bash
# mcpeval.sh — entry point for `make eval`: the agent eval harness
# (tools/mcpeval) against a live backend.
#
# On-demand only; never part of the blocking PR gate. Needs the `claude` CLI
# with Anthropic credentials (ANTHROPIC_API_KEY or `claude auth login`).
#
# Backend selection mirrors script/mcp-e2e.sh:
#   MELANGE_HOST + MCPEVAL_PAT_WRITE [+ MCPEVAL_PAT_READ]
#                         Use an existing backend untouched. Without a read
#                         PAT the scope-refusal task is skipped.
#   (neither set)         Bring-up mode: reuse mcp-e2e.sh's setup-only mode
#                         (compose backend + Airflow stub + seeded account,
#                         write and read PATs, public whisper fixture). The
#                         backend container/stub are torn down afterwards
#                         unless MCPEVAL_KEEP=1.
#
# Runner tunables pass through MCPEVAL_FLAGS, e.g.:
#   MCPEVAL_FLAGS="-task scope-refusal -model sonnet" make eval
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

WORK="$(mktemp -d)"
chmod 700 "$WORK"

STUB_PID=""
CONTAINER=""
BROUGHT_UP=0

cleanup() {
    status=$?
    set +e
    if [ "$BROUGHT_UP" -eq 1 ] && [ "${MCPEVAL_KEEP:-0}" != "1" ]; then
        [ -z "$STUB_PID" ] || kill "$STUB_PID" 2>/dev/null
        [ -z "$CONTAINER" ] || docker rm -f "$CONTAINER" >/dev/null 2>&1
    fi
    rm -rf "$WORK"
    exit "$status"
}
trap cleanup EXIT

if [ -n "${MELANGE_HOST:-}" ]; then
    if [ -z "${MCPEVAL_PAT_WRITE:-}" ]; then
        echo "FATAL: MELANGE_HOST is set but MCPEVAL_PAT_WRITE is not: an existing backend needs an existing write PAT" >&2
        exit 1
    fi
    echo "==> using existing backend $MELANGE_HOST"
else
    echo "==> bringing up the e2e backend (script/mcp-e2e.sh setup-only)"
    MCP_E2E_SETUP_ONLY=1 MCP_E2E_ENV_FILE="$WORK/env" ./script/mcp-e2e.sh
    # shellcheck disable=SC1091
    set -a && . "$WORK/env" && set +a
    STUB_PID="${MCPEVAL_STUB_PID:-}"
    CONTAINER="${MCPEVAL_CONTAINER:-}"
    BROUGHT_UP=1
fi

echo "==> building melange + mcpeval"
go build -o "$WORK/melange" ./cmd/melange
go build -o "$WORK/mcpeval" ./tools/mcpeval

# shellcheck disable=SC2086
"$WORK/mcpeval" -melange "$WORK/melange" ${MCPEVAL_FLAGS:-}
