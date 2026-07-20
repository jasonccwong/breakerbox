#!/usr/bin/env bash
# BreakerBox E2E: real hub + real agent + real supervised apps on this machine.
#
# Phase 1 core: enroll → import → toggle on → port live → toggle off → metrics.
# Phase 2 adds: crash auto-restart, log SSE, crash-loop → errored, ntfy
# delivery (local catcher), docker container lifecycle (skipped without
# docker), agent-crash resurrect with orphan reaping.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PORT=8095
APP_PORT=8123
NTFY_PORT=8971
LLM_PORT=8977
BASE="http://127.0.0.1:$PORT"
TMP="$(mktemp -d)"
HUB_DIR="$TMP/pb_data"
AGENT_DIR="$TMP/agent_state"
ADMIN_EMAIL="admin@e2e.local"
ADMIN_PASS="e2e-password-123"
DOCKER_CTR="bb-e2e-nginx"

cleanup() {
  jobs -p | xargs kill -9 2>/dev/null || true
  pkill -f "testapp -port $APP_PORT" 2>/dev/null || true
  docker rm -f "$DOCKER_CTR" >/dev/null 2>&1 || true
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

log "starting ntfy catcher on :$NTFY_PORT"
cat > "$TMP/catcher.py" <<'EOF'
import http.server, sys, threading
out = sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        with open(out, "a") as f:
            f.write(f"TITLE={self.headers.get('Title','')} PRIO={self.headers.get('Priority','')} BODY={body.decode(errors='replace')}\n")
        self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
EOF
python3 "$TMP/catcher.py" "$NTFY_PORT" "$TMP/ntfy.log" &

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
jsonval() { python3 -c "import sys,json;print(json.load(sys.stdin)$1)"; }

log "configuring ntfy endpoint"
SETTINGS_ID=$(auth_curl "$BASE/api/collections/settings/records" | jsonval '["items"][0]["id"]')
auth_curl -X PATCH "$BASE/api/collections/settings/records/$SETTINGS_ID" \
  -H 'Content-Type: application/json' \
  -d "{\"ntfy_endpoint\":\"http://127.0.0.1:$NTFY_PORT/alerts\"}" >/dev/null
auth_curl -X POST "$BASE/api/bb/notify/test" >/dev/null || fail "notify test route"
wait_until 5 "test notification delivered" grep -q "BreakerBox test" "$TMP/ntfy.log"
log "ntfy delivery verified"

log "minting enrollment token + enrolling agent"
ENROLL_TOKEN=$(auth_curl -X POST "$BASE/api/bb/enroll-tokens" | jsonval '["token"]')
export BREAKERBOX_STATE_DIR="$AGENT_DIR"
"$TMP/agent" enroll --hub "$BASE" --token "$ENROLL_TOKEN" --name e2e-host | grep -q enrolled || fail "agent enroll"

log "starting fake anthropic upstream on :$LLM_PORT"
cat > "$TMP/fake_llm.py" <<'EOF'
import http.server, json, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        _ = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        body = json.dumps({
            "id": "msg_e2e_proxy_1", "model": "claude-fable-5", "type": "message",
            "content": [{"type": "text", "text": "hello from fake upstream"}],
            "usage": {"input_tokens": 11, "output_tokens": 7,
                      "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
EOF
python3 "$TMP/fake_llm.py" "$LLM_PORT" &

log "starting agent daemon"
# Agent runs with a fixture HOME so tokenwatch reads controlled transcripts
# (created later in the token scenario), not this machine's real ~/.claude.
# The proxy upstream override points runtime metering at the fake provider.
FAKEHOME="$TMP/fakehome"
mkdir -p "$FAKEHOME"
HOME="$FAKEHOME" BREAKERBOX_UPSTREAM_ANTHROPIC="http://127.0.0.1:$LLM_PORT" \
  "$TMP/agent" run >"$TMP/agent.log" 2>&1 &
AGENT_PID=$!
wait_until 15 "system online" bash -c \
  "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/systems/records' | grep -q '\"status\":\"online\"'"

log "importing app definition (host-side approval path)"
cat > "$TMP/breakerbox.app.json" <<EOF
{
  "schema_version": 1,
  "name": "e2e-testapp",
  "kind": "process",
  "cmd": "$TMP/testapp",
  "args": ["-port", "$APP_PORT"],
  "cwd": "$TMP",
  "ports": [$APP_PORT],
  "stop": {"signal": "SIGTERM", "timeout_s": 5}
}
EOF
"$TMP/agent" apps import "$TMP/breakerbox.app.json" | grep -q queued || fail "apps import"
wait_until 20 "app registered+approved on hub" bash -c \
  "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records?filter=name=\"e2e-testapp\"' | grep -q '\"approval\":\"approved\"'"

APP_JSON=$(auth_curl "$BASE/api/collections/apps/records?filter=name=\"e2e-testapp\"")
APP_ID=$(echo "$APP_JSON" | jsonval '["items"][0]["id"]')
SYS_ID=$(echo "$APP_JSON" | jsonval '["items"][0]["system"]')
log "app=$APP_ID system=$SYS_ID"

send_cmd() { # send_cmd <app_id> <verb>
  auth_curl -X POST "$BASE/api/collections/commands/records" \
    -H 'Content-Type: application/json' \
    -d "{\"app\":\"$1\",\"system\":\"$SYS_ID\",\"verb\":\"$2\",\"status\":\"pending\"}" | jsonval '["id"]'
}
app_field() { auth_curl "$BASE/api/collections/apps/records/$1" | jsonval "[\"$2\"]"; }
wait_status() { # wait_status <app_id> <status> <desc>
  wait_until 20 "$3" bash -c \
    "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records/$1' | grep -q '\"status\":\"$2\"'"
}

log "toggle ON via commands API"
send_cmd "$APP_ID" start >/dev/null
wait_status "$APP_ID" running "app running"
wait_until 15 "app port serving" curl -sf "http://127.0.0.1:$APP_PORT/health"
PID1=$(app_field "$APP_ID" pid)
log "app running (pid $PID1) and serving on :$APP_PORT"

log "log SSE: tail + follow"
SSE=$(curl -sN --max-time 4 "$BASE/api/bb/apps/$APP_ID/logs?tail=50&token=$AUTH_TOKEN" || true)
echo "$SSE" | grep -q "listening on" || fail "log SSE did not deliver app output"
log "log streaming verified"

log "external kill -> crash-restart policy must bring it back"
kill -9 "$PID1"
wait_until 25 "app auto-restarted with new pid" bash -c \
  "P=\$(curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records/$APP_ID' | python3 -c 'import sys,json;print(json.load(sys.stdin)[\"pid\"])'); [ \"\$P\" != \"0\" ] && [ \"\$P\" != \"$PID1\" ]"
wait_status "$APP_ID" running "app running after crash"
wait_until 15 "port serving after crash-restart" curl -sf "http://127.0.0.1:$APP_PORT/health"
log "auto-restart verified (pid $PID1 -> $(app_field "$APP_ID" pid))"

log "agent crash -> resurrect with orphan reap"
kill -9 "$AGENT_PID"
sleep 1
curl -sf "http://127.0.0.1:$APP_PORT/health" >/dev/null || fail "orphan app should still be serving after agent death"
HOME="$FAKEHOME" BREAKERBOX_UPSTREAM_ANTHROPIC="http://127.0.0.1:$LLM_PORT" \
  "$TMP/agent" run >>"$TMP/agent.log" 2>&1 &
AGENT_PID=$!
wait_until 20 "system back online" bash -c \
  "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/systems/records' | grep -q '\"status\":\"online\"'"
wait_status "$APP_ID" running "app resurrected"
wait_until 15 "port serving after resurrect" curl -sf "http://127.0.0.1:$APP_PORT/health"
sleep 1
INSTANCES=$(pgrep -f "testapp -port $APP_PORT" | wc -l | tr -d ' ')
[ "$INSTANCES" = "1" ] || fail "expected exactly 1 testapp instance after resurrect, found $INSTANCES"
log "resurrect + orphan reap verified (single instance)"

log "toggle OFF"
send_cmd "$APP_ID" stop >/dev/null
wait_status "$APP_ID" stopped "app stopped via command"
curl -sf "http://127.0.0.1:$APP_PORT/health" 2>/dev/null && fail "port still serving after stop"
log "toggle off confirmed, port closed"

log "crash-loop app -> errored + ntfy alert"
CRASher_RESP=$(auth_curl -X POST "$BASE/api/bb/apps" -H 'Content-Type: application/json' -d "{
  \"system\": \"$SYS_ID\",
  \"definition\": {
    \"schema_version\": 1, \"name\": \"e2e-crasher\", \"kind\": \"process\",
    \"cmd\": \"/bin/sh\", \"args\": [\"-c\", \"exit 1\"],
    \"restart_policy\": {\"max_restarts\": 2, \"min_uptime_s\": 30, \"backoff_max_s\": 1}
  }}")
CRASHER_ID=$(echo "$CRASher_RESP" | jsonval '["app_id"]')
"$TMP/agent" apps approve "$CRASHER_ID" | grep -q queued || fail "approve crasher"
wait_until 20 "crasher approved" bash -c \
  "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records/$CRASHER_ID' | grep -q '\"approval\":\"approved\"'"
send_cmd "$CRASHER_ID" start >/dev/null
wait_status "$CRASHER_ID" errored "crasher gave up (errored)"
wait_until 10 "crash ntfy alert" bash -c "grep -q 'e2e-crasher' '$TMP/ntfy.log'"
log "crash-loop policy + alert verified"

if docker info >/dev/null 2>&1; then
  log "docker scenario: container app lifecycle"
  docker rm -f "$DOCKER_CTR" >/dev/null 2>&1 || true
  docker run -d --name "$DOCKER_CTR" nginx:alpine >/dev/null
  DK_RESP=$(auth_curl -X POST "$BASE/api/bb/apps" -H 'Content-Type: application/json' -d "{
    \"system\": \"$SYS_ID\",
    \"definition\": {
      \"schema_version\": 1, \"name\": \"e2e-nginx\", \"kind\": \"docker\",
      \"container_id\": \"$DOCKER_CTR\", \"stop\": {\"timeout_s\": 3}
    }}")
  DK_ID=$(echo "$DK_RESP" | jsonval '["app_id"]')
  # Docker kinds auto-approve agent-side (lifecycle of an existing container
  # is host-scoped); the approval_event must flow back to the hub.
  wait_until 20 "docker app auto-approved" bash -c \
    "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records/$DK_ID' | grep -q '\"approval\":\"approved\"'"
  send_cmd "$DK_ID" stop >/dev/null
  wait_status "$DK_ID" stopped "container stopped"
  [ "$(docker inspect -f '{{.State.Running}}' "$DOCKER_CTR")" = "false" ] || fail "engine still shows container running"
  send_cmd "$DK_ID" start >/dev/null
  wait_status "$DK_ID" running "container running"
  [ "$(docker inspect -f '{{.State.Running}}' "$DOCKER_CTR")" = "true" ] || fail "engine shows container stopped"
  log "docker lifecycle verified"
else
  log "docker unavailable — skipping container scenario"
fi

log "token tracking: fixture transcripts -> priced, attributed rows"
NOW_TS=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
CLAUDE_DIR="$FAKEHOME/.claude/projects/-e2e-proj"
CODEX_DIR="$FAKEHOME/.codex/sessions/$(date -u +%Y/%m/%d)"
mkdir -p "$CLAUDE_DIR" "$CODEX_DIR"
# Two Claude rows: one attributed (cwd = app cwd), one unmatched; plus garbage.
cat > "$CLAUDE_DIR/sess.jsonl" <<EOF
{"type":"assistant","cwd":"$TMP","sessionId":"e2e-sess","requestId":"req_e2e_1","timestamp":"$NOW_TS","message":{"id":"msg_e2e_1","model":"claude-fable-5","usage":{"input_tokens":100,"output_tokens":200,"cache_creation_input_tokens":1000,"cache_read_input_tokens":2000}}}
{not json garbage line
{"type":"assistant","cwd":"/nowhere/else","sessionId":"e2e-sess","requestId":"req_e2e_2","timestamp":"$NOW_TS","message":{"id":"msg_e2e_2","model":"claude-fable-5","usage":{"input_tokens":50,"output_tokens":60,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
EOF
# Codex cumulative counters -> one delta row.
cat > "$CODEX_DIR/rollout-e2e.jsonl" <<EOF
{"timestamp":"$NOW_TS","type":"session_meta","payload":{"type":"session_meta","id":"codex-e2e","cwd":"$TMP"}}
{"timestamp":"$NOW_TS","type":"turn_context","payload":{"type":"turn_context","model":"gpt-5.1-codex"}}
{"timestamp":"$NOW_TS","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":50,"total_tokens":1050}}}}
EOF

wait_until 40 "token rows ingested (scan interval 15s)" bash -c \
  "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/token_usage/records?perPage=1' | grep -q '\"totalItems\":3'"
TOKENS_JSON=$(auth_curl "$BASE/api/collections/token_usage/records?perPage=10")
echo "$TOKENS_JSON" | grep -q "\"app\":\"$APP_ID\"" || fail "no token row attributed to the app via cwd"
echo "$TOKENS_JSON" | grep -q '"app":""' || fail "unmatched row should sit in the system bucket"
echo "$TOKENS_JSON" | grep -q '"source":"codex"' || fail "codex delta row missing"
# claude-fable-5 pricing: 100*1e-5 + 200*5e-5 + 1000*1.25e-5 + 2000*1e-6 = 0.0255
python3 - "$TOKENS_JSON" <<'EOF'
import json, sys
rows = json.loads(sys.argv[1])["items"]
row = next(r for r in rows if r["dedup_key"] == "msg_e2e_1:req_e2e_1")
assert abs(row["cost_usd"] - 0.0255) < 1e-9, f"cost {row['cost_usd']} != 0.0255"
EOF
log "token pricing verified (\$0.0255 for fixture row)"

SUMMARY=$(auth_curl "$BASE/api/bb/tokens/summary?days=7")
echo "$SUMMARY" | grep -q '"claude-fable-5"' || fail "summary missing model breakdown"
echo "$SUMMARY" | python3 -c 'import sys,json; d=json.load(sys.stdin); assert d["totals"]["cost"] > 0.025' \
  || fail "summary totals wrong"
log "token summary endpoint verified"

log "spend alert: threshold below today's spend -> single ntfy"
auth_curl -X PATCH "$BASE/api/collections/settings/records/$SETTINGS_ID" \
  -H 'Content-Type: application/json' \
  -d '{"notify_spend": true, "spend_threshold_usd": 0.01}' >/dev/null
# New usage triggers the check on next ingest.
cat >> "$CLAUDE_DIR/sess.jsonl" <<EOF
{"type":"assistant","cwd":"$TMP","sessionId":"e2e-sess","requestId":"req_e2e_3","timestamp":"$NOW_TS","message":{"id":"msg_e2e_3","model":"claude-fable-5","usage":{"input_tokens":10,"output_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
EOF
wait_until 40 "spend threshold ntfy alert" bash -c "grep -q 'spend' '$TMP/ntfy.log'"
log "spend alert verified"

log "runtime proxy: metered app -> attributed runtime_proxy row"
LLM_APP_RESP=$(auth_curl -X POST "$BASE/api/bb/apps" -H 'Content-Type: application/json' -d "{
  \"system\": \"$SYS_ID\",
  \"definition\": {
    \"schema_version\": 1, \"name\": \"e2e-llm-app\", \"kind\": \"process\",
    \"cmd\": \"$TMP/testapp\", \"args\": [\"-llm-call\"]
  }}")
LLM_APP_ID=$(echo "$LLM_APP_RESP" | jsonval '["app_id"]')
"$TMP/agent" apps approve "$LLM_APP_ID" | grep -q queued || fail "approve llm app"
wait_until 20 "llm app approved" bash -c \
  "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/apps/records/$LLM_APP_ID' | grep -q '\"approval\":\"approved\"'"
auth_curl -X PATCH "$BASE/api/collections/apps/records/$LLM_APP_ID" \
  -H 'Content-Type: application/json' -d '{"token_tracking":"runtime"}' >/dev/null
sleep 1 # app_sync propagation
send_cmd "$LLM_APP_ID" start >/dev/null
wait_status "$LLM_APP_ID" running "llm app running"
wait_until 30 "runtime_proxy row for llm app" bash -c \
  "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/token_usage/records?filter=app=\"'$LLM_APP_ID'\"' | grep -q '\"source\":\"runtime_proxy\"'"
PROXY_ROW=$(auth_curl "$BASE/api/collections/token_usage/records?filter=app=\"$LLM_APP_ID\"")
echo "$PROXY_ROW" | grep -q '"dedup_key":"proxy:msg_e2e_proxy_1"' || fail "proxy dedup key wrong"
# claude-fable-5: 11*1e-5 + 7*5e-5 = 0.00046
echo "$PROXY_ROW" | python3 -c '
import sys, json
rows = json.load(sys.stdin)["items"]
assert any(abs(r["cost_usd"] - 0.00046) < 1e-9 for r in rows), rows
' || fail "proxy row cost wrong"
send_cmd "$LLM_APP_ID" stop >/dev/null
log "runtime proxy metering verified (\$0.00046, attributed via env injection)"

log "waiting for a metrics cycle (30s interval)"
wait_until 45 "system metrics rows" bash -c \
  "curl -sf -H 'Authorization: $AUTH_TOKEN' '$BASE/api/collections/system_metrics/records?perPage=1' | grep -q '\"totalItems\":[1-9]'"
log "metrics ingested"

log "ALL E2E ASSERTIONS PASSED ✅"
