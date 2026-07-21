#!/usr/bin/env bash
# rig.sh — local demo rig for the M6 mobile app (cloud/mobile).
#
# Brings up EVERYTHING the app needs against the orbstack dev stack:
#   1. port-forwards: orchestrator 127.0.0.1:18080, mockllm 127.0.0.1:18081
#   2. a seeded user + session token (Bearer auth for the app's login page)
#   3. a REAL `jcode web` device (temp HOME, mockllm model, E2EE on) logged in
#      via the real device-code flow — the relay target the app drives
#
# Usage:
#   scripts/rig.sh up       # idempotent; prints the login token at the end
#   scripts/rig.sh token    # print the session token again
#   scripts/rig.sh down     # stop processes and delete the seeded user
#
# The device HOME persists at /tmp/jmobile-rig so `up` is re-runnable (login
# is skipped once .jcode/cloud.json exists). Delete that dir for a fresh device.
set -euo pipefail

: "${KCTX:=orbstack}"
: "${NAMESPACE:=jcloud}"
: "${BASE:=http://127.0.0.1:18080}"
: "${MOCK_PORT:=18081}"
: "${WEB_PORT:=18086}"
: "${JCODE_BIN:=/Users/jack/workpath/jjj/jcode/jcode}"

RIG_HOME="${RIG_HOME:-/tmp/jmobile-rig}"
RIG_WS="${RIG_WS:-/tmp/jmobile-rig-ws}"
USER_ID="mobiledev00000000000000000000m6"
SESSION_ID="mobiledev00000000000000000000m7"
SESSION_TOKEN="jmobile-dev-session-token"

psql() {
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -tAc "$1"
}

listening() { lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }

pf() { # pf <local-port> <svc> <svc-port> <log>
  if listening "$1"; then echo "  port $1 already forwarded"; return 0; fi
  kubectl --context "$KCTX" -n "$NAMESPACE" port-forward "svc/$2" "$1:$3" >"$4" 2>&1 &
  for _ in $(seq 1 20); do listening "$1" && return 0; sleep 0.5; done
  echo "  ERROR: port-forward $2 never came up (log: $4)" >&2; return 1
}

cmd_up() {
  echo "== port-forwards =="
  pf 18080 orchestrator 8080 /tmp/jmobile-pf-orch.log
  pf "$MOCK_PORT" mockllm 8081 /tmp/jmobile-pf-mock.log

  echo "== seed user/session =="
  local hash
  hash="$(printf '%s' "$SESSION_TOKEN" | shasum -a 256 | awk '{print $1}')"
  psql "INSERT INTO users (id, display_name, avatar_url, is_cluster_admin, created_at)
          VALUES ('$USER_ID', 'M6 Mobile Dev', '', false, now())
          ON CONFLICT (id) DO NOTHING;
        INSERT INTO sessions (id, user_id, token_hash, created_at, expires_at, revoked_at)
          VALUES ('$SESSION_ID', '$USER_ID', '$hash', now(), now() + interval '30 days', NULL)
          ON CONFLICT (id) DO UPDATE
            SET token_hash=EXCLUDED.token_hash, expires_at=EXCLUDED.expires_at, revoked_at=NULL;" >/dev/null
  local me_code
  me_code="$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $SESSION_TOKEN" "$BASE/api/v1/me")"
  [ "$me_code" = "200" ] || { echo "  ERROR: seeded session does not resolve (me -> $me_code)" >&2; exit 1; }
  echo "  session resolves (GET /api/v1/me -> 200)"

  mkdir -p "$RIG_HOME/.jcode" "$RIG_WS"

  if [ ! -f "$RIG_HOME/.jcode/config.json" ]; then
    echo "== write mockllm config (E2EE on) =="
    cat >"$RIG_HOME/.jcode/config.json" <<JSON
{
  "providers": {
    "mock": {
      "api_key": "dummy-key",
      "base_url": "http://127.0.0.1:$MOCK_PORT/v1",
      "custom_models": [
        { "id": "mock-model", "name": "mock-model", "tool_call": true, "context": 128000 }
      ]
    }
  },
  "model": "mock/mock-model",
  "default_mode": "full_access",
  "memory": { "enabled": false }
}
JSON
  fi

  if [ ! -f "$RIG_HOME/.jcode/cloud.json" ]; then
    echo "== device-code login =="
    local log="$RIG_HOME/login.log" cli_user="" rc=""
    HOME="$RIG_HOME" "$JCODE_BIN" login --cloud "$BASE" --name m6-mobile-rig >"$log" 2>&1 &
    local login_pid=$!
    for _ in $(seq 1 30); do
      cli_user="$(grep -oE '[A-Z2-9]{4}-[A-Z2-9]{4}' "$log" 2>/dev/null | head -1 || true)"
      [ -n "$cli_user" ] && break
      kill -0 "$login_pid" 2>/dev/null || break
      sleep 1
    done
    [ -n "$cli_user" ] || { echo "  ERROR: no user_code (log: $log)" >&2; exit 1; }
    curl -sS -H 'Content-Type: application/json' -H "Authorization: Bearer $SESSION_TOKEN" \
      -d "{\"user_code\":\"$cli_user\",\"approve\":true}" "$BASE/auth/device/authorize" | grep -q approved \
      || { echo "  ERROR: approve failed for $cli_user" >&2; exit 1; }
    for _ in $(seq 1 30); do
      if ! kill -0 "$login_pid" 2>/dev/null; then wait "$login_pid"; rc=$?; break; fi
      sleep 1
    done
    [ "$rc" = "0" ] || { echo "  ERROR: jcode login rc=$rc" >&2; exit 1; }
    echo "  logged in: $(jq -r .device_id "$RIG_HOME/.jcode/cloud.json")"
  else
    echo "== device already logged in: $(jq -r .device_id "$RIG_HOME/.jcode/cloud.json") =="
  fi

  echo "== jcode web (port $WEB_PORT) =="
  if ! listening "$WEB_PORT"; then
    ( cd "$RIG_WS" && exec env HOME="$RIG_HOME" "$JCODE_BIN" web \
        --port "$WEB_PORT" --host 127.0.0.1 --open=false \
        >>"$RIG_HOME/web.log" 2>&1 ) &
    for _ in $(seq 1 30); do
      curl -sS -o /dev/null "http://127.0.0.1:$WEB_PORT/api/health" 2>/dev/null && break
      sleep 1
    done
  fi
  curl -sS -o /dev/null "http://127.0.0.1:$WEB_PORT/api/health" \
    && echo "  jcode web healthy" || { echo "  ERROR: jcode web not healthy (log: $RIG_HOME/web.log)" >&2; exit 1; }

  echo
  echo "== devices visible to the session =="
  curl -sS -H "Authorization: Bearer $SESSION_TOKEN" "$BASE/api/v1/devices"
  echo
  echo
  echo "MOBILE LOGIN → cloud URL: $BASE   token: $SESSION_TOKEN"
}

cmd_token() { printf '%s\n' "$SESSION_TOKEN"; }

cmd_down() {
  pkill -f "jcode web --port $WEB_PORT" 2>/dev/null || true
  pkill -f "port-forward svc/orchestrator 18080" 2>/dev/null || true
  pkill -f "port-forward svc/mockllm $MOCK_PORT" 2>/dev/null || true
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -c "DELETE FROM users WHERE id='$USER_ID'" >/dev/null 2>&1 || true
  echo "rig down (kept $RIG_HOME; rm -rf it for a fresh device)"
}

case "${1:-}" in
  up) cmd_up ;;
  token) cmd_token ;;
  down) cmd_down ;;
  *) echo "usage: $0 up|token|down" >&2; exit 2 ;;
esac
