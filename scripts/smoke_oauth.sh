#!/usr/bin/env bash
# Smoke tests for feat/oauth-primary-login
# Offline-only — no network to https://api.zetic.ai
set -eu
# Disable pipefail for grep -q SIGPIPE (grep exits early, writer gets SIGPIPE 141)
set +o pipefail 2>/dev/null || true
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="/tmp/melange-smoke"
echo "Building $BIN..."
go -C "$ROOT" build -o "$BIN" ./cmd/melange

echo "=== HELP ==="
"$BIN" auth login --help | grep -qi "browser" && echo "PASS login help browser" || (echo "FAIL login help"; exit 1)
"$BIN" auth status --help | grep -qi "auth" && echo "PASS status help"
"$BIN" auth token --help | grep -qi "token" && echo "PASS token help"
"$BIN" auth logout --help | grep -qi "logout" && echo "PASS logout help"

echo "=== ENV PRECEDENCE ==="
MELANGE_API_KEY=ztp_envtest XDG_CONFIG_HOME=$(mktemp -d) APPDATA=$(mktemp -d) "$BIN" auth token | grep -q "ztp_envtest" && echo "PASS env precedence" || (echo "FAIL env"; exit 1)

TMP=$(mktemp); echo -n "ztp_file123" > "$TMP"
MELANGE_API_KEY_FILE="$TMP" XDG_CONFIG_HOME=$(mktemp -d) APPDATA=$(mktemp -d) env -u MELANGE_API_KEY "$BIN" auth token | grep -q "ztp_file123" && echo "PASS file precedence" || (echo "FAIL file"; exit 1)
rm "$TMP"

TMP=$(mktemp); echo -n "   " > "$TMP"
XDG_CONFIG_HOME=$(mktemp -d) APPDATA=$(mktemp -d) env -u MELANGE_API_KEY MELANGE_API_KEY_FILE="$TMP" "$BIN" auth token 2>&1 | grep -q "no token" && echo "PASS empty file falls through" || (echo "FAIL empty file"; exit 1)
rm "$TMP"

echo "=== NON-TTY EXIT 2 ==="
env -u MELANGE_API_KEY -u MELANGE_API_KEY_FILE XDG_CONFIG_HOME=$(mktemp -d) APPDATA=$(mktemp -d) sh -c "echo | $BIN auth login 2>&1" | grep -q "cannot prompt" && echo "PASS non-tty exit2"

echo "=== CONFIG OAUTH ROUNDTRIP ==="
go -C "$ROOT" test ./internal/config -run TestSetHostOAuthRoundTrip -v 2>&1 | grep -q "PASS" && echo "PASS config roundtrip"

echo "=== PKCE 43 chars ==="
go -C "$ROOT" test ./internal/oauth -run TestPKCE -v 2>&1 | grep -q "PASS" && echo "PASS pkce"

echo "=== INVALID_TARGET RETRY ==="
go -C "$ROOT" test ./internal/oauth -run TestInvalidTargetRetry -v 2>&1 | grep -q "PASS" && echo "PASS invalid_target"

echo "=== ALL SMOKE PASS ==="
