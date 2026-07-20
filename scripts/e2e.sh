#!/usr/bin/env bash
# BreakerBox walking-skeleton E2E: real hub + real agent + real supervised app.
# Asserts: enroll → import → toggle on → port live → external kill detected →
# toggle on/off via commands API → metrics rows exist.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT=8095
APP_PORT=8123
BASE="http://127.0.0.1:$PORT"
TMP="$(mktemp -d)"
HUB_DIR="$TMP/pb_data"
AGENT_DIR="$TMP/agent_state"
ADMIN_EMAIL="admin@e2e.local"
ADMIN_PASS="e2e-password-123"

cleanup() {
  jobs -p | xargs kill -9 2>/dev/null || true
  pkill -f "testapp -port $APP_PORT" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

log()  { printf '\033[1;34m[e2e]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[e2e FAIL]\033[0m %s\n' "$*"; exit 1; }

wait_until() { # wait_until <seconds> <desc> <cmd...>
  local deadline=$(( $(date +%s) + $1 )); shift
  local desc="$1"; shift
  while ! "$@" >/dev/null 2>&1; do
    [ "$(date +%s)" -ge "$deadline" ] && fail "timeout waiting for: $desc"
    sleep 0.5
  done
}

log "building binaries"
(cd "$ROOT/hub" && go build -o "$TMP/hub" .)
(cd "$ROOT/agent" && go build -o "$TMP/agent" .)
(cd "$ROOT/cmd/testapp" && go build -o "$TMP/testapp" .)

log "starting hub on :$PORT"
"$TMP/hub" serve --http="127.0.0.1:$PORT" --dir="$HUB_DIR" >"$TMP/hub.log" 2>&1 &
wait_until 15 "hub health" curl -sf "$BASE/api/bb/health"

log "creating superuser"
"$TMP/hub" superuser upsert "$ADMIN_EMAIL" "$ADMIN_PASS" --dir="$HUB_DIR" >/dev/null

AUTH_TOKEN=$(curl -sf -X POST "$BASE/api/collections/_superusers/auth-with-password" \
  -H 'Content-Type: application/json' \
  -d "{\"identity\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
[ -n "$AUTH_TOKEN" ] || fail "superuser auth"
auth_curl() { curl -sf -H "Authorization: $AUTH_TOKEN" "$@"; }

log "minting enrollment token"
ENROLL_TOKEN=$(auth_curl -X POST "$BASE/api/bb/enroll-tokens" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
[ -n "$ENROLL_TOKEN" ] || fail "mint enrollment token"

log "enrolling agent"
export BREAKERBOX_STATE_DIR="$AGENT_DIR"
"$TMP/agent" enroll --hub "$BASE" --token "$ENROLL_TOKEN" --name e2e-host | grep -q enrolled || fail "agent enroll"

log "starting agent daemon"
"$TMP/agent" run >"$TMP/agent.log" 2>&1 &
wait_until 15 "system online" bash -c \
  "auth() { curl -sf -H 'Authorization: $AUTH_TOKEN' \"\$@\"; }; auth '$BASE/api/collections/systems/records' | grep -q '\"status\":\"online\"'"

log "importing app definition (host-side approval path)"
cat > "$TMP/breakerbox.app.json" <<EOF
{
  "schema_version": 1,
  "name": "e2e-testapp",
  "kind": "process",
  "cmd": "$TMP/testapp",
  "args": ["-port", "$APP_PORT"],
  "ports": [$APP_PORT],
  "stop": {"signal": "SIGTERM", "timeout_s": 5}
}
EOF
"$TMP/agent" apps import "$TMP/breakerbox.app.json" | grep -q queued || fail "apps import"

wait_until 20 "app registered+approved on hub" bash -c \
  "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records' | grep -q '\"approval\":\"approved\"'"

APP_JSON=$(auth_curl "$BASE/api/collections/apps/records")
APP_ID=$(echo "$APP_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)["items"][0]["id"])')
SYS_ID=$(echo "$APP_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)["items"][0]["system"])')
log "app=$APP_ID system=$SYS_ID"

send_cmd() { # send_cmd <verb>
  auth_curl -X POST "$BASE/api/collections/commands/records" \
    -H 'Content-Type: application/json' \
    -d "{\"app\":\"$APP_ID\",\"system\":\"$SYS_ID\",\"verb\":\"$1\",\"status\":\"pending\"}" \
    | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])'
}
app_status() { auth_curl "$BASE/api/collections/apps/records/$APP_ID" | python3 -c 'import sys,json;print(json.load(sys.stdin)["status"])'; }
cmd_status() { auth_curl "$BASE/api/collections/commands/records/$1" | python3 -c 'import sys,json;print(json.load(sys.stdin)["status"])'; }

log "toggle ON via commands API"
CMD_ID=$(send_cmd start)
wait_until 15 "command done" bash -c "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/commands/records/$CMD_ID' | grep -q '\"status\":\"done\"'"
wait_until 15 "app running" bash -c "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records/$APP_ID' | grep -q '\"status\":\"running\"'"
wait_until 15 "app port serving" curl -sf "http://127.0.0.1:$APP_PORT/health"
log "app is running and serving on :$APP_PORT"

log "external kill -> panel must flip to stopped"
APP_PID=$(auth_curl "$BASE/api/collections/apps/records/$APP_ID" | python3 -c 'import sys,json;print(int(json.load(sys.stdin)["pid"]))')
kill -9 "$APP_PID"
wait_until 15 "app stopped after external kill" bash -c "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records/$APP_ID' | grep -q '\"status\":\"stopped\"'"
log "external kill detected"

log "toggle ON again, then OFF"
send_cmd start >/dev/null
wait_until 15 "app running again" bash -c "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records/$APP_ID' | grep -q '\"status\":\"running\"'"
send_cmd stop >/dev/null
wait_until 15 "app stopped via command" bash -c "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records/$APP_ID' | grep -q '\"status\":\"stopped\"'"
curl -sf "http://127.0.0.1:$APP_PORT/health" 2>/dev/null && fail "port still serving after stop"
log "toggle off confirmed, port closed"

log "waiting for a metrics cycle (30s interval)"
wait_until 45 "system metrics rows" bash -c \
  "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/system_metrics/records?perPage=1' | grep -q '\"totalItems\":[1-9]'"
log "metrics ingested"

log "ALL E2E ASSERTIONS PASSED ✅"
