#!/usr/bin/env bash
# mcp-e2e.sh — live end-to-end proof of `melange mcp` against a real backend.
#
# Drives BOTH transports with raw protocol traffic (no SDK client): the stdio
# transport as newline-delimited JSON-RPC on a held-open pipe, and the HTTP
# transport as stateless bearer-authenticated POSTs. Every leg records
# PASS/FAIL/SKIP into the summary table at the end; any FAIL exits nonzero.
# Run it via `make e2e`. Deliberately NOT part of the blocking PR gate: this
# needs a real backend and (in bring-up mode) Docker.
#
# ── Env contract ────────────────────────────────────────────────────────────
#   MELANGE_HOST          Existing backend base URL (e.g. http://127.0.0.1:8000).
#                         When set, Docker is never touched and MELANGE_E2E_PAT
#                         is REQUIRED. When unset, bring-up mode starts the
#                         compose backend (db + server) itself.
#   MELANGE_E2E_PAT       PAT (ztp_...) every leg authenticates with. Required
#                         with MELANGE_HOST; optional in bring-up mode, where a
#                         local-only PAT is seeded into the compose DB via
#                         manage.py (never printed, never persisted outside the
#                         run's 0700 work dir).
#   MCP_E2E_BACKEND_DIR   Backend checkout for bring-up mode (default:
#                         ../zetic_backend next to this repo). Must contain
#                         docker-compose.yml and a .env.prod (gitignored; real
#                         GCS SA + DB_HOST=db + an e2e DB_NAME, per the
#                         backend's e2e notes).
#   MCP_E2E_BACKEND_PORT  Host port for the backend container (default 18000).
#   MCP_E2E_STUB_PORT     Host port for the Airflow dispatch stub (default
#                         18100; see script/mcp-e2e-airflow-stub.py).
#   MCP_E2E_HF_REPO       Public HF repo for the import leg (default
#                         Qwen/Qwen2.5-0.5B-Instruct; import verifies it live
#                         against huggingface.co, so network is required).
#   MCP_E2E_SKIP_IMPORT   =1 marks the import + conversion-status legs SKIP
#                         (for a MELANGE_HOST backend with no Airflow behind
#                         it, where dispatch deterministically 500s).
#   MCP_E2E_KEEP          =1 keeps the backend container running afterwards.
#   MCP_E2E_SETUP_ONLY    =1 stops after bring-up + seeding + auth: no legs
#                         run, the backend container and Airflow stub are left
#                         running, and the connection env (host, account, both
#                         PATs, stub pid) is written to MCP_E2E_ENV_FILE
#                         (0600) for another driver — tools/mcpeval reuses
#                         this instead of reimplementing bring-up.
#   MCP_E2E_ENV_FILE      Path that receives the setup-only env file
#                         (required with MCP_E2E_SETUP_ONLY=1).
#
# Idempotent re-runs: every resource is run-scoped (repo mcp-e2e-run-<id>) and
# stragglers from earlier failed runs (repos matching mcp-e2e-run-*, the
# mcp-e2e-server container) are purged on start. The compose db container is
# left running on exit — it is a shared dev database, not ours to stop.
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

BACKEND_DIR="${MCP_E2E_BACKEND_DIR:-$ROOT/../zetic_backend}"
BACKEND_PORT="${MCP_E2E_BACKEND_PORT:-18000}"
STUB_PORT="${MCP_E2E_STUB_PORT:-18100}"
HF_REPO="${MCP_E2E_HF_REPO:-Qwen/Qwen2.5-0.5B-Instruct}"
SKIP_IMPORT="${MCP_E2E_SKIP_IMPORT:-0}"
KEEP="${MCP_E2E_KEEP:-0}"
SETUP_ONLY="${MCP_E2E_SETUP_ONLY:-0}"
ENV_FILE="${MCP_E2E_ENV_FILE:-}"
SERVER_CONTAINER="mcp-e2e-server"
RUN_ID="$(date +%s)-$$"
RUN_REPO="mcp-e2e-run-${RUN_ID}"
FIXTURES_REPO="mcp-e2e-fixtures"

WORK="$(mktemp -d)"
chmod 700 "$WORK"
mkdir -p "$WORK/secret" "$WORK/legs"
chmod 700 "$WORK/secret"

STUB_PID=""
HTTP_PID=""
STARTED_CONTAINER=0
PAT=""
PAT_READ=""
HOST=""
ACCOUNT=""
HTTP_ADDR=""
HTTP_CODE=""

fatal() { echo "FATAL: $*" >&2; exit 1; }
note() { echo "==> $*"; }

need() {
    command -v "$1" >/dev/null 2>&1 || fatal "missing prerequisite: $1 ($2)"
}
need jq "JSON assertions"
need curl "API calls"
need go "building the melange binary"

if [ "$SETUP_ONLY" = "1" ] && [ -z "$ENV_FILE" ]; then
    fatal "MCP_E2E_SETUP_ONLY=1 needs MCP_E2E_ENV_FILE=<path> to receive the connection env"
fi

# ── Result collection ───────────────────────────────────────────────────────
RESULTS=()
FAILED=0
record() { # status name detail
    RESULTS+=("$1|$2|$3")
    case "$1" in
    FAIL) FAILED=1 ;;
    esac
    printf '  %-4s  %s — %s\n' "$1" "$2" "$3"
}

summary() {
    echo ""
    echo "== mcp-e2e summary (run ${RUN_ID}) =="
    printf '%-6s %-34s %s\n' STATUS LEG DETAIL
    printf '%-6s %-34s %s\n' ------ ---------------------------------- ------
    local row
    for row in "${RESULTS[@]}"; do
        printf '%-6s %-34s %s\n' "${row%%|*}" "$(echo "$row" | cut -d'|' -f2)" "${row##*|}"
    done
    echo ""
    if [ "$FAILED" -ne 0 ]; then echo "RESULT: FAIL"; else echo "RESULT: PASS"; fi
}

cleanup() {
    status=$?
    set +e
    # Delete every repo this run (or a straggler) created.
    if [ -n "$PAT" ] && [ -n "$HOST" ] && [ -n "$ACCOUNT" ]; then
        for repo in $(api_get "/v1/repos?search=mcp-e2e-run-&limit=100" 2>/dev/null |
            jq -r '.results[]?.name // empty' 2>/dev/null | grep '^mcp-e2e-run-'); do
            curl -fsS -m 15 -X DELETE -H "Authorization: Bearer $PAT" \
                "$HOST/v1/repos/$ACCOUNT/$repo" >/dev/null 2>&1 &&
                echo "    cleaned up repo $ACCOUNT/$repo"
        done
    fi
    if [ -n "$HTTP_PID" ]; then
        kill -INT "$HTTP_PID" 2>/dev/null
        wait "$HTTP_PID" 2>/dev/null
    fi
    if [ -n "$STUB_PID" ]; then
        kill "$STUB_PID" 2>/dev/null
        wait "$STUB_PID" 2>/dev/null
    fi
    if [ "$STARTED_CONTAINER" -eq 1 ] && [ "$KEEP" != "1" ]; then
        docker rm -f "$SERVER_CONTAINER" >/dev/null 2>&1
    fi
    rm -rf "$WORK"
    exit "$status"
}
trap cleanup EXIT

# ── Plain API helpers (direct backend calls, used for byte-exact baselines
#    and cleanup — never through the MCP server) ────────────────────────────
api_get() { curl -fsS -m 20 -H "Authorization: Bearer $PAT" "$HOST$1"; }

# ── Backend: existing host or compose bring-up ──────────────────────────────
wait_backend() {
    local i code
    for i in $(seq 1 60); do
        code="$(curl -sS -m 3 -o /dev/null -w '%{http_code}' "$HOST/v1/me" 2>/dev/null)" || code=000
        case "$code" in 200 | 401) return 0 ;; esac
        sleep 1
    done
    return 1
}

if [ -n "${MELANGE_HOST:-}" ]; then
    HOST="${MELANGE_HOST%/}"
    PAT="${MELANGE_E2E_PAT:-}"
    [ -n "$PAT" ] || fatal "MELANGE_HOST is set but MELANGE_E2E_PAT is not: an existing backend needs an existing PAT (bring-up mode can seed one, a remote host cannot)"
    note "using existing backend $HOST"
    wait_backend || fatal "backend $HOST did not answer /v1/me within 60s"
else
    need docker "bring-up mode (or set MELANGE_HOST)"
    need python3 "the Airflow dispatch stub"
    [ -f "$BACKEND_DIR/docker-compose.yml" ] ||
        fatal "no docker-compose.yml in $BACKEND_DIR — set MCP_E2E_BACKEND_DIR to the zetic_backend checkout or MELANGE_HOST to an existing backend"
    [ -f "$BACKEND_DIR/.env.prod" ] ||
        fatal "$BACKEND_DIR/.env.prod is missing — bring-up mode needs the gitignored e2e env file (real GCS SA, DB_HOST=db, an e2e DB_NAME); see the backend's e2e notes"
    HOST="http://127.0.0.1:$BACKEND_PORT"

    note "purging straggler container from earlier runs"
    docker rm -f "$SERVER_CONTAINER" >/dev/null 2>&1 || true

    note "starting compose db (idempotent)"
    docker network inspect zetic-shared-network >/dev/null 2>&1 ||
        docker network create zetic-shared-network >/dev/null
    (cd "$BACKEND_DIR" && docker compose up -d db >/dev/null 2>&1) ||
        fatal "docker compose up -d db failed in $BACKEND_DIR"

    if ! docker image inspect zetic_backend-server:latest >/dev/null 2>&1; then
        note "building backend image (first run only)"
        (cd "$BACKEND_DIR" && docker compose build server) ||
            fatal "docker compose build server failed"
    fi

    # Purge a straggler stub (e.g. from a crashed setup-only run) so the bind
    # below succeeds instead of silently losing to the old process.
    lsof -ti tcp:"$STUB_PORT" 2>/dev/null | xargs kill 2>/dev/null || true

    note "starting Airflow dispatch stub on :$STUB_PORT"
    python3 "$ROOT/script/mcp-e2e-airflow-stub.py" "$STUB_PORT" \
        >"$WORK/stub.log" 2>&1 &
    STUB_PID=$!

    note "starting backend container $SERVER_CONTAINER on :$BACKEND_PORT"
    # Plain `docker run` rather than compose: compose's env_file injection
    # keeps quotes literally, while the app's own dotenv loader (DOTENV_CONFIG
    # is baked into the image) parses .env.prod correctly — so env comes from
    # the bind-mounted file, and only targeted overrides ride the process env:
    # ENV=production un-no-ops the Airflow dispatch (DEBUG skips it) and
    # AIRFLOW_HOST points that dispatch at the stub.
    docker run -d --name "$SERVER_CONTAINER" \
        --network zetic-shared-network \
        --add-host host.docker.internal:host-gateway \
        -p "$BACKEND_PORT:80" \
        -v "$BACKEND_DIR":/app \
        -e POETRY_VIRTUALENVS_IN_PROJECT=false \
        -e ENV=production \
        -e AIRFLOW_HOST="http://host.docker.internal:$STUB_PORT" \
        zetic_backend-server:latest >/dev/null ||
        fatal "docker run failed for $SERVER_CONTAINER"
    STARTED_CONTAINER=1

    wait_backend || {
        docker logs "$SERVER_CONTAINER" 2>&1 | tail -20 >&2
        fatal "backend container never became reachable on $HOST"
    }

    note "applying migrations (idempotent)"
    docker exec -e POETRY_VIRTUALENVS_IN_PROJECT=false "$SERVER_CONTAINER" \
        poetry run python manage.py migrate >"$WORK/migrate.log" 2>&1 ||
        fatal "manage.py migrate failed: $(tail -3 "$WORK/migrate.log" | tr '\n' ' ')"

    if [ -n "${MELANGE_E2E_PAT:-}" ]; then
        PAT="$MELANGE_E2E_PAT"
        note "using PAT from MELANGE_E2E_PAT"
    else
        note "seeding the mcp-e2e account + PAT + READY fixture model (idempotent)"
        # The fixture model gives the billable-download and deployment-guide
        # legs a converted target to act on: conversions cannot finish in this
        # environment (the stub never runs the DAG), so a READY row with one
        # ORT target artifact is seeded the same way the pipeline would have
        # written it. The PAT never crosses stdout: it goes through a file
        # inside the container into the run's 0700 work dir.
        if ! docker exec -i -e POETRY_VIRTUALENVS_IN_PROJECT=false "$SERVER_CONTAINER" \
            poetry run python manage.py shell >"$WORK/seed.log" 2>&1 <<'PYSEED'
import uuid
from account.models import Account, Project, User
from model_management.models import CommonModel, TargetModel
from zetic.auth.services import TokenService

user = User.objects.filter(username="mcp-e2e").first()
if user is None:
    user = User.objects.create_user(
        username="mcp-e2e", email="mcp-e2e@example.com", password=uuid.uuid4().hex
    )
user.is_staff = True
user.bypass_billing_plan_limits = True
user.save()
account = Account.objects.filter(user=user).first()
if account is None:
    account = Account.objects.create(name="mcp-e2e", user=user, type=Account.Type.USER)

repo = Project.objects.filter(
    account=account, name="mcp-e2e-fixtures", deleted_at__isnull=True
).first()
if repo is None:
    repo = Project.objects.create(
        account=account, name="mcp-e2e-fixtures", is_private=False, tags=[], model_type=0
    )
model = CommonModel.objects.filter(project=repo, deleted_at__isnull=True).first()
if model is None:
    model = CommonModel.objects.create(
        project=repo,
        tag=uuid.uuid4().hex,
        status=CommonModel.ModelStatus.READY,
        type="general",
        version=1,
        source="uploads/mcp-e2e-seed/model.onnx",
        source_type="manual",
        metadata={"name": "mcp-e2e-seed"},
        job_args={},
    )
target = TargetModel.objects.filter(parent=model, deleted_at__isnull=True).first()
if target is None:
    TargetModel.objects.create(
        parent=model,
        target=TargetModel.Target.ZETIC_MLANGE_TARGET_ORT,
        uri="target_models/mcp-e2e-seed/model.onnx",
        download_size=1024,
        checksums=["9e107d9d372bb6826bd81d3542a419d6"],
        remote_paths=["target_models/mcp-e2e-seed/model.onnx"],
    )

# A PUBLIC whisper fixture for tools/mcpeval's library-to-deploy task: the
# shared e2e DB has no real playground repos, so the library search needs one
# seeded entry with a READY model and a converted target (same pattern as the
# mcp-e2e-fixtures repo above; conversions cannot finish here).
lib_repo = Project.objects.filter(
    account=account, name="whisper-tiny-mcpeval", deleted_at__isnull=True
).first()
if lib_repo is None:
    lib_repo = Project.objects.create(
        account=account,
        name="whisper-tiny-mcpeval",
        is_private=False,
        tags=["speech-recognition"],
        model_type=0,
        use_case="speech",
        description="Whisper tiny speech-to-text, converted for on-device inference.",
        readme="# whisper-tiny\n\nOpenAI Whisper tiny, converted and benchmarked for on-device speech recognition.",
    )
lib_model = CommonModel.objects.filter(project=lib_repo, deleted_at__isnull=True).first()
if lib_model is None:
    lib_model = CommonModel.objects.create(
        project=lib_repo,
        tag=uuid.uuid4().hex,
        status=CommonModel.ModelStatus.READY,
        type="general",
        version=1,
        source="uploads/mcpeval-whisper/model.onnx",
        source_type="manual",
        metadata={"name": "whisper-tiny"},
        job_args={},
    )
lib_target = TargetModel.objects.filter(parent=lib_model, deleted_at__isnull=True).first()
if lib_target is None:
    TargetModel.objects.create(
        parent=lib_model,
        target=TargetModel.Target.ZETIC_MLANGE_TARGET_ORT,
        uri="target_models/mcpeval-whisper/model.onnx",
        download_size=2048,
        checksums=["9e107d9d372bb6826bd81d3542a419d6"],
        remote_paths=["target_models/mcpeval-whisper/model.onnx"],
    )

service = TokenService.factory()
token = next((t for t in service.list(user) if t.name == "mcp-e2e-script"), None)
if token is None:
    token = service.create(user, "mcp-e2e-script", ["write"])
with open("/tmp/mcp-e2e-pat", "w") as fh:
    fh.write(token.hash)
# A read-only PAT alongside the write one: tools/mcpeval's scope-refusal task
# needs a credential whose /v1/me grant is exactly ["read"].
read_token = next((t for t in service.list(user) if t.name == "mcp-e2e-readonly"), None)
if read_token is None:
    read_token = service.create(user, "mcp-e2e-readonly", ["read"])
with open("/tmp/mcp-e2e-pat-read", "w") as fh:
    fh.write(read_token.hash)
print("SEEDED")
PYSEED
        then
            fatal "seeding failed: $(tail -3 "$WORK/seed.log" | tr '\n' ' ')"
        fi
        grep -q SEEDED "$WORK/seed.log" ||
            fatal "seeding did not confirm: $(tail -3 "$WORK/seed.log" | tr '\n' ' ')"
        docker exec "$SERVER_CONTAINER" cat /tmp/mcp-e2e-pat >"$WORK/secret/pat"
        docker exec "$SERVER_CONTAINER" cat /tmp/mcp-e2e-pat-read >"$WORK/secret/pat-read"
        docker exec "$SERVER_CONTAINER" rm -f /tmp/mcp-e2e-pat /tmp/mcp-e2e-pat-read
        chmod 600 "$WORK/secret/pat" "$WORK/secret/pat-read"
        PAT="$(cat "$WORK/secret/pat")"
        PAT_READ="$(cat "$WORK/secret/pat-read")"
    fi
fi

case "$PAT" in
ztp_*) : ;;
*) fatal "the PAT does not look like a personal access token (expected ztp_ prefix)" ;;
esac
if ! api_get /v1/me >"$WORK/secret/me.json" 2>/dev/null; then
    fatal "the PAT was rejected by $HOST/v1/me — seed or supply a working MELANGE_E2E_PAT"
fi
ACCOUNT="$(jq -r '.account.name' "$WORK/secret/me.json")"
note "authenticated as account '$ACCOUNT'"

note "purging straggler repos from earlier failed runs"
for repo in $(api_get "/v1/repos?search=mcp-e2e-run-&limit=100" |
    jq -r '.results[]?.name // empty' | grep '^mcp-e2e-run-' || true); do
    curl -fsS -m 15 -X DELETE -H "Authorization: Bearer $PAT" \
        "$HOST/v1/repos/$ACCOUNT/$repo" >/dev/null 2>&1 &&
        echo "    purged $ACCOUNT/$repo"
done

# Setup-only mode: everything a downstream driver needs is up — hand over the
# connection env and leave the backend + stub running. tools/mcpeval consumes
# this instead of duplicating bring-up/seeding.
if [ "$SETUP_ONLY" = "1" ]; then
    if [ -n "$PAT_READ" ]; then
        if ! curl -fsS -m 20 -H "Authorization: Bearer $PAT_READ" "$HOST/v1/me" >/dev/null 2>&1; then
            fatal "the seeded read-only PAT was rejected by $HOST/v1/me"
        fi
    fi
    umask 077
    {
        echo "MELANGE_HOST=$HOST"
        echo "MCPEVAL_ACCOUNT=$ACCOUNT"
        echo "MCPEVAL_PAT_WRITE=$PAT"
        echo "MCPEVAL_PAT_READ=$PAT_READ"
        echo "MCPEVAL_STUB_PID=$STUB_PID"
        if [ "$STARTED_CONTAINER" -eq 1 ]; then
            echo "MCPEVAL_CONTAINER=$SERVER_CONTAINER"
        else
            echo "MCPEVAL_CONTAINER="
        fi
    } >"$ENV_FILE"
    note "setup-only: backend on $HOST (account $ACCOUNT); env written to $ENV_FILE"
    # The container and stub deliberately outlive this script; the caller owns
    # teardown (pids/names are in the env file). Clearing the handles keeps
    # the EXIT trap from reaping them.
    KEEP=1
    STUB_PID=""
    exit 0
fi

note "building melange"
go build -o "$WORK/melange" ./cmd/melange

# ── stdio transport driver ──────────────────────────────────────────────────
# One process per call, held open through a FIFO: frames go in, the pipe stays
# open until the response with the awaited id lands (or the deadline passes),
# then closing stdin ends the session cleanly (exit 0).
stdio_await() { # await-id deadline-seconds frames... -> matching line on stdout
    local await="$1" deadline="$2" fifo="$WORK/legs/fifo" out="$WORK/legs/stdio-out" pid i
    shift 2
    rm -f "$fifo"
    mkfifo "$fifo"
    : >"$out"
    MELANGE_API_KEY="$PAT" MELANGE_HOST="$HOST" "$WORK/melange" mcp \
        <"$fifo" >"$out" 2>>"$WORK/stdio-server.log" &
    pid=$!
    exec 3>"$fifo"
    printf '%s\n' "$@" >&3
    i=0
    while [ "$i" -lt $((deadline * 5)) ]; do
        if jq -e --argjson id "$await" 'select(.id==$id)' "$out" >/dev/null 2>&1; then
            break
        fi
        sleep 0.2
        i=$((i + 1))
    done
    exec 3>&-
    wait "$pid" 2>/dev/null || true
    rm -f "$fifo"
    jq -c --argjson id "$await" 'select(.id==$id)' "$out"
    cat "$out" >>"$WORK/legs/stdio-transcript.ndjson"
    rm -f "$out"
}

INITIALIZE_FRAME='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcp-e2e","version":"0.0.0"}}}'
INITIALIZED_FRAME='{"jsonrpc":"2.0","method":"notifications/initialized"}'

stdio_rpc() { # request-json deadline -> response line
    stdio_await 2 "${2:-60}" "$INITIALIZE_FRAME" "$INITIALIZED_FRAME" "$1"
}

stdio_call() { # tool arguments-json deadline -> response line
    stdio_rpc "$(jq -nc --arg name "$1" --argjson args "$2" \
        '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:$name,arguments:$args}}')" "${3:-60}"
}

# ── HTTP transport driver ───────────────────────────────────────────────────
http_post() { # body [bearer] -> body on stdout; status in HTTP_CODE; headers in $WORK/legs/http-headers
    local body="$1" bearer="${2:-}"
    local auth=()
    [ -n "$bearer" ] && auth=(-H "Authorization: Bearer $bearer")
    HTTP_CODE="$(curl -sS -m 30 -o "$WORK/legs/http-body" -D "$WORK/legs/http-headers" \
        -w '%{http_code}' -X POST "http://$HTTP_ADDR/" \
        -H "Content-Type: application/json" \
        -H "Accept: application/json, text/event-stream" \
        ${auth[@]+"${auth[@]}"} -d "$body")"
    cat "$WORK/legs/http-body"
}

# ═══════════════════════════════════ stdio legs ═════════════════════════════
echo ""
note "stdio legs (raw JSON-RPC over a held-open pipe)"

# S1: initialize handshake — the server must identify itself and speak a
# protocol version.
INIT="$(stdio_await 1 20 "$INITIALIZE_FRAME")"
SERVER_NAME="$(echo "$INIT" | jq -r '.result.serverInfo.name // empty')"
PROTO="$(echo "$INIT" | jq -r '.result.protocolVersion // empty')"
if [ -n "$SERVER_NAME" ] && [ -n "$PROTO" ]; then
    record PASS "stdio initialize" "serverInfo.name=$SERVER_NAME protocolVersion=$PROTO"
else
    record FAIL "stdio initialize" "no initialize result: $(echo "$INIT" | head -c 160)"
fi

# S2: tools/list must be the frozen 18-tool stdio catalog.
EXPECTED_STDIO_TOOLS="create_repo delete_repo get_account_info get_conversion_status get_deployment_info get_library_model get_model get_model_report get_repo import_model list_models list_repos request_model_download search_library set_default_model update_repo upload_model whoami"
LIST="$(stdio_rpc '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' 20)"
GOT_TOOLS="$(echo "$LIST" | jq -r '.result.tools[].name' | sort | tr '\n' ' ' | sed 's/ $//')"
COUNT="$(echo "$LIST" | jq '.result.tools | length')"
if [ "$COUNT" = 18 ] && [ "$GOT_TOOLS" = "$EXPECTED_STDIO_TOOLS" ]; then
    record PASS "stdio tools/list" "18 tools, catalog exact (upload_model present)"
else
    record FAIL "stdio tools/list" "count=$COUNT; got: $GOT_TOOLS"
fi

# S3: whoami passes GET /v1/me through byte-exact. token.last_used_at is the
# one volatile field (every authenticated request bumps it), so it is masked
# on BOTH sides; everything else must match byte-for-byte (jq -c on each side
# is one identical compaction, so equality proves identical JSON incl. key
# order).
WHOAMI="$(stdio_call whoami '{}' 30)"
MCP_ME="$(echo "$WHOAMI" | jq -c '.result.structuredContent | del(.token.last_used_at)')"
API_ME="$(api_get /v1/me | jq -c 'del(.token.last_used_at)')"
if [ -n "$MCP_ME" ] && [ "$MCP_ME" != null ] && [ "$MCP_ME" = "$API_ME" ]; then
    record PASS "stdio whoami passthrough" "byte-exact vs GET /v1/me (last_used_at masked)"
else
    record FAIL "stdio whoami passthrough" "mcp=$MCP_ME api=$API_ME"
fi

# S4: create_repo (llm, run-scoped name — the tool refuses an account prefix,
# so NAME alone goes in).
CREATE="$(stdio_call create_repo "$(jq -nc --arg n "$RUN_REPO" '{name:$n, model_type:"llm", description:"melange mcp-e2e run-scoped repo"}')" 30)"
FULL_NAME="$(echo "$CREATE" | jq -r '.result.structuredContent.full_name // empty')"
if [ "$FULL_NAME" = "$ACCOUNT/$RUN_REPO" ]; then
    record PASS "stdio create_repo" "created $FULL_NAME (model_type=llm)"
else
    record FAIL "stdio create_repo" "unexpected result: $(echo "$CREATE" | jq -c '.result.content[0].text // .' | head -c 200)"
fi

# S5: get_repo byte-exact vs the API (a stable read: repo timestamps do not
# move between the two calls).
REPO_MCP="$(stdio_call get_repo "$(jq -nc --arg r "$ACCOUNT/$RUN_REPO" '{repo:$r}')" 30 | jq -c '.result.structuredContent')"
REPO_API="$(api_get "/v1/repos/$ACCOUNT/$RUN_REPO" | jq -c .)"
if [ -n "$REPO_MCP" ] && [ "$REPO_MCP" != null ] && [ "$REPO_MCP" = "$REPO_API" ]; then
    record PASS "stdio get_repo passthrough" "byte-exact vs GET /v1/repos/$ACCOUNT/$RUN_REPO"
else
    record FAIL "stdio get_repo passthrough" "mcp=$REPO_MCP api=$REPO_API"
fi

# S6+S7: import_model, then get_conversion_status with a bounded wait. The
# conversion can never finish here (bring-up mode's Airflow stub only accepts
# the dispatch; it runs no DAG), so "converting" IS the deterministic
# expectation of this environment and the bounded wait proves the poll returns
# the latest state instead of hanging.
MODEL_KEY=""
if [ "$SKIP_IMPORT" = "1" ]; then
    record SKIP "stdio import_model" "MCP_E2E_SKIP_IMPORT=1 (backend without Airflow dispatch)"
    record SKIP "stdio get_conversion_status" "skipped with import"
else
    IMPORT="$(stdio_call import_model "$(jq -nc --arg r "$ACCOUNT/$RUN_REPO" --arg hf "$HF_REPO" '{repo:$r, hf_repo:$hf}')" 180)"
    MODEL_KEY="$(echo "$IMPORT" | jq -r '.result.structuredContent.key // empty')"
    IMPORT_STATE="$(echo "$IMPORT" | jq -r '.result.structuredContent.state // empty')"
    if [ -n "$MODEL_KEY" ] && [ "$IMPORT_STATE" = "converting" ]; then
        record PASS "stdio import_model" "imported $HF_REPO -> key=$MODEL_KEY state=converting"
    else
        record FAIL "stdio import_model" "$(echo "$IMPORT" | jq -c '.result.content[0].text // .error // .' | head -c 220)"
    fi

    if [ -n "$MODEL_KEY" ]; then
        STATUS="$(stdio_call get_conversion_status "$(jq -nc --arg r "$ACCOUNT/$RUN_REPO" --arg k "$MODEL_KEY" '{repo:$r, model_key:$k, wait_seconds:5}')" 40)"
        STATE="$(echo "$STATUS" | jq -r '.result.structuredContent.state // empty')"
        case "$STATE" in
        converting | optimizing | ready | failed)
            record PASS "stdio get_conversion_status" "bounded wait (5s) returned state=$STATE"
            ;;
        *)
            record FAIL "stdio get_conversion_status" "$(echo "$STATUS" | jq -c '.result.content[0].text // .' | head -c 220)"
            ;;
        esac
    else
        record SKIP "stdio get_conversion_status" "no model key from import"
    fi
fi

# Fixture model discovery (bring-up mode seeds it; a MELANGE_HOST backend may
# or may not have one). It backs the deployment-guide and billable-download
# legs, which need a model with a converted target.
FIX_KEY="$(api_get "/v1/repos/$ACCOUNT/$FIXTURES_REPO/models" 2>/dev/null |
    jq -r '.results[0].key // empty' 2>/dev/null || true)"
FIX_TARGET=""
if [ -n "$FIX_KEY" ]; then
    FIX_TARGET="$(api_get "/v1/repos/$ACCOUNT/$FIXTURES_REPO/models/$FIX_KEY/targets" |
        jq -r '.results[0].target_id // empty')"
fi

# S8: get_deployment_info — catalog mode always; guide mode via the fixture.
DEPLOY="$(stdio_call get_deployment_info '{}' 30)"
DEPLOY_KEYS="$(echo "$DEPLOY" | jq -c '.result.structuredContent | keys? // []' 2>/dev/null || echo '[]')"
if echo "$DEPLOY" | jq -e '(.result.structuredContent | type == "object") and ((.result.isError // false) | not)' >/dev/null 2>&1; then
    if [ -n "$FIX_KEY" ]; then
        GUIDE="$(stdio_call get_deployment_info "$(jq -nc --arg r "$ACCOUNT/$FIXTURES_REPO" --arg k "$FIX_KEY" '{repo:$r, model_key:$k}')" 30)"
        if echo "$GUIDE" | jq -e '(.result.structuredContent | type == "object") and ((.result.isError // false) | not)' >/dev/null 2>&1; then
            record PASS "stdio get_deployment_info" "catalog keys=$DEPLOY_KEYS; fixture guide ok"
        else
            record FAIL "stdio get_deployment_info" "guide failed: $(echo "$GUIDE" | jq -c '.result.content[0].text // .' | head -c 180)"
        fi
    else
        record PASS "stdio get_deployment_info" "catalog keys=$DEPLOY_KEYS; guide untested (no fixture model)"
    fi
else
    record FAIL "stdio get_deployment_info" "$(echo "$DEPLOY" | jq -c '.result.content[0].text // .' | head -c 200)"
fi

# S9: request_model_download refuses without confirm — and must say so
# without having authorized anything.
if [ -n "$FIX_KEY" ] && [ -n "$FIX_TARGET" ]; then
    REFUSE="$(stdio_call request_model_download "$(jq -nc --arg r "$ACCOUNT/$FIXTURES_REPO" --arg k "$FIX_KEY" --arg t "$FIX_TARGET" '{repo:$r, model_key:$k, target_id:$t}')" 30)"
    if echo "$REFUSE" | jq -e '.result.isError == true' >/dev/null 2>&1 &&
        echo "$REFUSE" | jq -r '.result.content[0].text' | grep -q "nothing was authorized"; then
        record PASS "stdio download confirm gate" "unconfirmed call refused, nothing authorized"
    else
        record FAIL "stdio download confirm gate" "$(echo "$REFUSE" | jq -c '.result.content[0].text // .' | head -c 200)"
    fi

    # S10: confirmed billable download — every signed artifact URL must come
    # back redacted (the default; include_urls stays off in an e2e transcript).
    DOWNLOAD="$(stdio_call request_model_download "$(jq -nc --arg r "$ACCOUNT/$FIXTURES_REPO" --arg k "$FIX_KEY" --arg t "$FIX_TARGET" '{repo:$r, model_key:$k, target_id:$t, confirm:true}')" 60)"
    AUTH_ID="$(echo "$DOWNLOAD" | jq -r '.result.structuredContent.authorization_id // empty')"
    URLS="$(echo "$DOWNLOAD" | jq -r '[.result.structuredContent.artifacts[].url] | unique | join(",")' 2>/dev/null || true)"
    if [ -n "$AUTH_ID" ] && [ "$URLS" = "<redacted>" ]; then
        record PASS "stdio request_model_download" "authorized (id=$AUTH_ID), every url=<redacted>"
    else
        record FAIL "stdio request_model_download" "auth_id='$AUTH_ID' urls='$URLS' $(echo "$DOWNLOAD" | jq -c '.result.content[0].text // .' | head -c 160)"
    fi
else
    record SKIP "stdio download confirm gate" "no fixture model with a converted target on this backend"
    record SKIP "stdio request_model_download" "no fixture model with a converted target on this backend"
fi

# S11: delete_repo refuses without confirm and the repo must survive.
NODEL="$(stdio_call delete_repo "$(jq -nc --arg r "$ACCOUNT/$RUN_REPO" '{repo:$r}')" 30)"
STILL="$(curl -sS -m 10 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $PAT" "$HOST/v1/repos/$ACCOUNT/$RUN_REPO")"
if echo "$NODEL" | jq -e '.result.isError == true' >/dev/null 2>&1 &&
    echo "$NODEL" | jq -r '.result.content[0].text' | grep -q "Nothing was deleted" &&
    [ "$STILL" = 200 ]; then
    record PASS "stdio delete confirm gate" "unconfirmed call refused; repo still exists"
else
    record FAIL "stdio delete confirm gate" "isError/text mismatch or repo gone (GET=$STILL)"
fi

# S12: delete_repo with the exact ACCOUNT/NAME confirm deletes it for real.
DEL="$(stdio_call delete_repo "$(jq -nc --arg r "$ACCOUNT/$RUN_REPO" '{repo:$r, confirm:$r}')" 30)"
GONE="$(curl -sS -m 10 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $PAT" "$HOST/v1/repos/$ACCOUNT/$RUN_REPO")"
if [ "$(echo "$DEL" | jq -c '.result.structuredContent')" = "{\"deleted\":true,\"repo\":\"$ACCOUNT/$RUN_REPO\"}" ] && [ "$GONE" = 404 ]; then
    record PASS "stdio delete_repo" "confirmed delete; repo 404s afterwards"
else
    record FAIL "stdio delete_repo" "result=$(echo "$DEL" | jq -c '.result.structuredContent // .' | head -c 160) GET=$GONE"
fi

# ═══════════════════════════════════ HTTP legs ══════════════════════════════
echo ""
note "HTTP legs (stateless bearer POSTs)"

"$WORK/melange" mcp --transport http --listen 127.0.0.1:0 --host "$HOST" \
    2>"$WORK/http-server.log" &
HTTP_PID=$!
for _ in $(seq 1 50); do
    HTTP_ADDR="$(grep -m1 -o 'addr=[0-9.]*:[0-9]*' "$WORK/http-server.log" 2>/dev/null | cut -d= -f2 || true)"
    if [ -n "$HTTP_ADDR" ] && curl -fsS -m 2 "http://$HTTP_ADDR/healthz" >/dev/null 2>&1; then
        break
    fi
    HTTP_ADDR=""
    sleep 0.2
done
[ -n "$HTTP_ADDR" ] || fatal "melange mcp --transport http never became healthy"

# H1: /healthz liveness (unauthenticated by design).
HEALTH="$(curl -sS -m 5 "http://$HTTP_ADDR/healthz")"
if echo "$HEALTH" | jq -e '.status == "ok"' >/dev/null 2>&1; then
    record PASS "http /healthz" "$(echo "$HEALTH" | jq -c .)"
else
    record FAIL "http /healthz" "$HEALTH"
fi

# H2: tools/list is the frozen 17-tool HTTP catalog — upload_model must be
# absent (the server cannot see the caller's files).
EXPECTED_HTTP_TOOLS="create_repo delete_repo get_account_info get_conversion_status get_deployment_info get_library_model get_model get_model_report get_repo import_model list_models list_repos request_model_download search_library set_default_model update_repo whoami"
HLIST="$(http_post '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' "$PAT")"
HGOT="$(echo "$HLIST" | jq -r '.result.tools[].name' | sort | tr '\n' ' ' | sed 's/ $//')"
HCOUNT="$(echo "$HLIST" | jq '.result.tools | length')"
if [ "$HCOUNT" = 17 ] && [ "$HGOT" = "$EXPECTED_HTTP_TOOLS" ]; then
    record PASS "http tools/list" "17 tools, catalog exact, upload_model absent"
else
    record FAIL "http tools/list" "count=$HCOUNT; got: $HGOT"
fi

# H3: whoami over HTTP resolves the same identity from the request bearer.
HWHO="$(http_post '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whoami","arguments":{}}}' "$PAT")"
H_ME="$(echo "$HWHO" | jq -c '.result.structuredContent | del(.token.last_used_at)')"
API_ME2="$(api_get /v1/me | jq -c 'del(.token.last_used_at)')"
if [ -n "$H_ME" ] && [ "$H_ME" != null ] && [ "$H_ME" = "$API_ME2" ]; then
    record PASS "http whoami passthrough" "byte-exact vs GET /v1/me (last_used_at masked)"
else
    record FAIL "http whoami passthrough" "mcp=$H_ME api=$API_ME2"
fi

# H4: get_repo over HTTP, byte-exact against the API (fixtures repo when the
# backend has one, else the account's newest visible repo).
BYTE_REPO="$FIXTURES_REPO"
[ -n "$FIX_KEY" ] || BYTE_REPO="$(api_get "/v1/repos?limit=1" | jq -r '.results[0].name // empty')"
if [ -n "$BYTE_REPO" ]; then
    HREPO="$(http_post "$(jq -nc --arg r "$ACCOUNT/$BYTE_REPO" '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:"get_repo",arguments:{repo:$r}}}')" "$PAT" | jq -c '.result.structuredContent')"
    AREPO="$(api_get "/v1/repos/$ACCOUNT/$BYTE_REPO" | jq -c .)"
    if [ -n "$HREPO" ] && [ "$HREPO" != null ] && [ "$HREPO" = "$AREPO" ]; then
        record PASS "http get_repo passthrough" "byte-exact vs GET /v1/repos/$ACCOUNT/$BYTE_REPO"
    else
        record FAIL "http get_repo passthrough" "mcp=$HREPO api=$AREPO"
    fi
else
    record SKIP "http get_repo passthrough" "no repo visible to compare against"
fi

# H5: a bearer-less request is a 401 carrying the RFC 9110 WWW-Authenticate
# challenge (bare Bearer scheme — no resource is configured on this server).
http_post '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' >/dev/null || true
CHALLENGE="$(grep -i '^www-authenticate:' "$WORK/legs/http-headers" | tr -d '\r' | head -1)"
if [ "$HTTP_CODE" = 401 ] && echo "$CHALLENGE" | grep -qi 'bearer'; then
    record PASS "http 401 challenge" "401 + '$CHALLENGE'"
else
    record FAIL "http 401 challenge" "code=$HTTP_CODE challenge='$CHALLENGE'"
fi

# H6: a machine-speed burst with one token trips the per-token limiter (burst
# 40, 2/s refill) — expect 429s with an integral Retry-After. Last HTTP leg
# on purpose: it empties this token's bucket.
BURST_429=0
RETRY_AFTER=""
for _ in $(seq 1 50); do
    http_post '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' "$PAT" >/dev/null || true
    if [ "$HTTP_CODE" = 429 ]; then
        BURST_429=$((BURST_429 + 1))
        [ -n "$RETRY_AFTER" ] || RETRY_AFTER="$(grep -i '^retry-after:' "$WORK/legs/http-headers" | tr -d '\r' | awk '{print $2}')"
    fi
done
if [ "$BURST_429" -gt 0 ] && [ -n "$RETRY_AFTER" ]; then
    record PASS "http 429 burst" "$BURST_429/50 throttled, Retry-After=${RETRY_AFTER}s"
else
    record FAIL "http 429 burst" "no 429 in a 50-request burst (throttled=$BURST_429)"
fi

kill -INT "$HTTP_PID" 2>/dev/null || true
wait "$HTTP_PID" 2>/dev/null || true
HTTP_PID=""

# ═══════════════════════════════ hygiene self-check ═════════════════════════
# Nothing this run captured — leg outputs, both server logs, the stdio
# transcript, seed/migrate output — may contain the bearer. The PAT itself
# lives only under $WORK/secret (0700), which is excluded.
LEAKS="$(grep -R -F -l -- "$PAT" "$WORK" 2>/dev/null | grep -v "/secret/" || true)"
if [ -z "$LEAKS" ]; then
    record PASS "credential hygiene" "PAT bytes absent from every captured output/log"
else
    record FAIL "credential hygiene" "PAT found in: $LEAKS"
fi

summary
[ "$FAILED" -eq 0 ]
