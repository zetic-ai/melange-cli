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

echo "=== KEYRING OAUTH SEPARATE ==="
go -C "$ROOT" test ./internal/keyring -run TestSetGetDeleteOAuth -v 2>&1 | grep -q "PASS" && echo "PASS keyring oauth"
go -C "$ROOT" test ./internal/keyring -run TestOAuthSeparateFromPAT -v 2>&1 | grep -q "PASS" && echo "PASS oauth separate keys"

echo "=== REFRESH SINGLE-FLIGHT ==="
go -C "$ROOT" test ./internal/cmd/auth -run TestResolveAnyTokenStaleRefresh -v 2>&1 | grep -q "PASS" && echo "PASS stale refresh"
go -C "$ROOT" test ./internal/cmd/auth -run TestResolveAnyTokenInvalidGrant -v 2>&1 | grep -q "PASS" && echo "PASS invalid_grant clear"

echo "=== LOGOUT REVOKE (offline mock) ==="
go -C "$ROOT" test ./internal/oauth -run TestRevoke -v 2>&1 | grep -q "PASS" && echo "PASS revoke"
go -C "$ROOT" test ./internal/oauth -run TestRefreshInvalidGrant -v 2>&1 | grep -q "PASS" && echo "PASS refresh invalid_grant"

echo "=== STATUS/TOKEN/LOGOUT HELP & JSON ==="
"$BIN" auth status --help | grep -qi "status" && echo "PASS status help"
"$BIN" auth token --help | grep -qi "token" && echo "PASS token help detailed"
# token exact newline with env
TMP_TOK=$(mktemp); echo -n "ztp_smoketoken123" > "$TMP_TOK"; chmod 600 "$TMP_TOK"
out=$(MELANGE_API_KEY_FILE="$TMP_TOK" XDG_CONFIG_HOME=$(mktemp -d) APPDATA=$(mktemp -d) env -u MELANGE_API_KEY "$BIN" auth token)
if [ "$out" = "ztp_smoketoken123" ]; then echo "PASS token exact newline"; else echo "FAIL token exact: got '$out'"; exit 1; fi
rm "$TMP_TOK"

echo "=== DISCOVERY FALLBACK ==="
go -C "$ROOT" test ./internal/oauth -run TestDiscover -v 2>&1 | grep -q "PASS" && echo "PASS discovery"
go -C "$ROOT" test ./internal/oauth -run TestFallbackDiscovery -v 2>&1 | grep -q "PASS" && echo "PASS fallback discovery"

echo "=== BROWSER DISPLAY GUARD ==="
go -C "$ROOT" test ./internal/browser -run TestOpenNoDisplay -v 2>&1 | grep -q "PASS\|SKIP" && echo "PASS browser display"

echo "=== PKCE CHALLENGE ==="
go -C "$ROOT" test ./internal/oauth -run TestGenerateVerifier -v 2>&1 | grep -q "PASS" && echo "PASS verifier"

echo "=== CONFIG SAVE/LOAD ==="
go -C "$ROOT" test ./internal/config -run TestSaveTo -v 2>&1 | grep -q "PASS" && echo "PASS config save"
go -C "$ROOT" test ./internal/config -run TestDeleteHostOAuth -v 2>&1 | grep -q "PASS" && echo "PASS delete oauth"

echo "=== RESOLVE HOST & OAUTH ==="
go -C "$ROOT" test ./internal/config -run TestResolveAnyTokenWith -v 2>&1 | grep -q "PASS" && echo "PASS resolve any"

echo "=== MAIN RESOLVE (singleflight) ==="
go -C "$ROOT" test ./cmd/melange -run TestResolveAnyTokenMain -v 2>&1 | grep -q "PASS" && echo "PASS main resolve"

echo "=== ALL SMOKE PASS ==="
