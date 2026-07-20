#!/usr/bin/env bash
# j8-device-relay.sh — Journey J8 "device relay" (design contract:
# cloud/docs/17-jcode-device-relay.md §4, module M3). Drives the FULL relay
# loop against the live orchestrator with a REAL `jcode web` process (temp
# HOME, mockllm as the model): the connector registers, heartbeats, long-polls
# downlink commands, forwards them to the local web control plane, and pumps
# local WS events uplink (durable batched + ephemeral fire-and-forget).
#
# Asserts (docs/17 §4.1/§4.2/§4.3/§4.4):
#   J8-S1  device online: GET /api/v1/devices (user session) shows the device
#          online=true after the connector registers+heartbeats; negatives:
#          CONSOLE_TOKEN (service principal) list -> 400; unknown device -> 404
#   J8-S2  remote chat.send: POST .../sessions/new/messages -> 202 with a
#          command_id; the session index gains a session that reaches idle;
#          the same session exists in the LOCAL jcode web (/api/sessions)
#   J8-S3  durable replay: GET .../sessions/{sid}/events has user_message /
#          tool_call / tool_result / agent_done; seqs are 1..N gapless; the
#          user_message payload carries data.source="console" (channel marker)
#   J8-S4  live SSE: GET .../devices/{id}/stream emits an initial
#          device.status(online) frame, then session.event (durable notify)
#          and session.delta (ephemeral agent_text) frames for a new message
#   J8-S5  chat.stop + approval.respond: stop -> 202 and the command reaches
#          `acked` (device_commands); approval.respond -> 202 and reaches a
#          terminal state (`failed` here: the approval_id is deliberately
#          bogus, so the local control plane 404s and the connector acks
#          "error" — still proving the downlink→local→ack loop)
#   J8-S6  offline: after killing `jcode web`, the device flips online=false
#          within TTL+margin (<=150s); POST messages/stop then -> 409
#          device_offline
#   J8-S7  reconnect: restarting `jcode web` (same HOME) brings the device
#          back online; a new message lands and durable seqs CONTINUE past the
#          pre-restart max (server-seeded 续号, no restart at 1, still gapless)
#
# Model: the in-cluster mockllm (svc/mockllm:8081, scenario write_file) behind
# a scratch port-forward, wired as the only provider in the temp HOME's
# .jcode/config.json (mock/mock-model, full_access so no approval is needed).
# Device credentials come from a REAL `jcode login --cloud $BASE` device-code
# flow, approved via the seeded user session (same psql seeding as j7).
#
# Sourced by e2e.sh (BASE/TOKEN exported) OR runnable standalone:
#   BASE=http://127.0.0.1:18080 ./j8-device-relay.sh
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

: "${JCODE_BIN:=$HERE/../../jcode/jcode}"
: "${J8_MOCK_PORT:=18081}"   # scratch port-forward to svc/mockllm:8081
: "${J8_WEB_PORT:=18086}"    # local `jcode web` bind (loopback only)

J8_USER_ID="e2edevice0000000000000000000000j8"
J8_SESSION_ID="e2edevice0000000000000000000000j9"
J8_SESSION_TOKEN="e2e-device-relay-session-token"

# Run-scoped state (also used by j8_cleanup from e2e.sh's teardown trap).
J8_HOME=""; J8_WS=""; J8_WEB_PID=""; J8_MOCK_PF_PID=""; J8_DEVICE_ID=""

j8_psql() {
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -tAc "$1"
}

j8_seed_session() {
  local hash
  hash="$(printf '%s' "$J8_SESSION_TOKEN" | shasum -a 256 | awk '{print $1}')"
  j8_psql "INSERT INTO users (id, display_name, avatar_url, is_cluster_admin, created_at)
             VALUES ('$J8_USER_ID', 'e2e-relay-tester', '', false, now())
             ON CONFLICT (id) DO NOTHING;
           INSERT INTO sessions (id, user_id, token_hash, created_at, expires_at, revoked_at)
             VALUES ('$J8_SESSION_ID', '$J8_USER_ID', '$hash', now(), now() + interval '1 day', NULL)
             ON CONFLICT (id) DO UPDATE
               SET token_hash=EXCLUDED.token_hash, expires_at=EXCLUDED.expires_at, revoked_at=NULL;" \
    >/dev/null
}

# j8_cleanup stops the local processes (jcode web, mockllm port-forward) and
# drops the throwaway user; ON DELETE CASCADE removes its sessions, devices,
# device_tokens, device_sessions, device_events and device_commands. Safe to
# call when partially set up (all members tolerate empty state).
j8_cleanup() {
  [ -n "$J8_WEB_PID" ] && kill "$J8_WEB_PID" 2>/dev/null
  [ -n "$J8_MOCK_PF_PID" ] && kill "$J8_MOCK_PF_PID" 2>/dev/null
  [ -n "$J8_HOME" ] && rm -rf "$J8_HOME"
  [ -n "$J8_WS" ] && rm -rf "$J8_WS"
  J8_WEB_PID=""; J8_MOCK_PF_PID=""; J8_HOME=""; J8_WS=""
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -c "DELETE FROM users WHERE id='$J8_USER_ID'" >/dev/null 2>&1 || true
}

# User-session-authenticated helpers (the client API docs/17 §4.3 requires a
# real user session, not the CONSOLE_TOKEN service principal).
j8_get_code() {
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $J8_SESSION_TOKEN" "$BASE$1"
}
j8_post_code() {
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $J8_SESSION_TOKEN" \
    -H 'Content-Type: application/json' -d "$2" "$BASE$1"
}

# j8_wait_command <command_id> -> polls device_commands until the command
# leaves pending/delivered; prints the terminal status (acked|failed|…).
j8_wait_command() {
  local cid="$1" st="" i
  for i in $(seq 1 30); do
    st="$(j8_psql "SELECT status FROM device_commands WHERE id='$cid'" 2>/dev/null | tr -d '[:space:]')"
    case "$st" in acked|failed|canceled) printf '%s' "$st"; return 0;; esac
    sleep 1
  done
  printf '%s' "$st"; return 1
}

j8_run() {
  section "J8 · device relay (docs/17 §4 — M3, real jcode web + mockllm)"

  if [ ! -x "$JCODE_BIN" ]; then
    skip J8-S1 "jcode binary not found at $JCODE_BIN (build: make -C ../jcode build-binary)"
    return 0
  fi

  # --- setup: seed, port-forward mockllm, temp HOME, real login -------------
  j8_cleanup
  if ! j8_seed_session; then
    fail J8-S1 "could not seed the e2e-relay-tester user/session into Postgres"
    return 1
  fi
  local me_code
  me_code="$(http_code "$(j8_get_code "/api/v1/me")")"
  if [ "$me_code" != "200" ]; then
    fail J8-S1 "seeded session does not resolve (GET /api/v1/me -> $me_code)"
    return 1
  fi

  kubectl --context "$KCTX" -n "$NAMESPACE" port-forward svc/mockllm \
    "$J8_MOCK_PORT:8081" >/tmp/j8-mockllm-pf.log 2>&1 &
  J8_MOCK_PF_PID=$!
  local mock_ready="false" i
  for i in $(seq 1 20); do
    if curl -sS -o /dev/null "http://127.0.0.1:$J8_MOCK_PORT/health" 2>/dev/null; then
      mock_ready="true"; break
    fi
    sleep 1
  done
  if [ "$mock_ready" != "true" ]; then
    fail J8-S1 "mockllm port-forward never became healthy (log: $(tail -2 /tmp/j8-mockllm-pf.log))"
    return 1
  fi
  info "  mockllm reachable at 127.0.0.1:$J8_MOCK_PORT"

  J8_HOME="$(mktemp -t j8-home.XXXXXX)"; rm -f "$J8_HOME"; mkdir -p "$J8_HOME/.jcode"
  J8_WS="$(mktemp -t j8-ws.XXXXXX)"; rm -f "$J8_WS"; mkdir -p "$J8_WS"
  cat >"$J8_HOME/.jcode/config.json" <<JSON
{
  "providers": {
    "mock": {
      "api_key": "dummy-key",
      "base_url": "http://127.0.0.1:$J8_MOCK_PORT/v1",
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

  # Real device-code login (same scrape-approve pattern as J7-S8).
  local log="$J8_HOME/login.log" login_pid cli_user="" login_rc=""
  HOME="$J8_HOME" "$JCODE_BIN" login --cloud "$BASE" --name e2e-j8 >"$log" 2>&1 &
  login_pid=$!
  for i in $(seq 1 30); do
    cli_user="$(grep -oE '[A-Z2-9]{4}-[A-Z2-9]{4}' "$log" 2>/dev/null | head -1)"
    [ -n "$cli_user" ] && break
    kill -0 "$login_pid" 2>/dev/null || break
    sleep 1
  done
  if [ -z "$cli_user" ]; then
    fail J8-S1 "jcode login did not print a user_code (log: $(tail -3 "$log"))"
    kill "$login_pid" 2>/dev/null; wait "$login_pid" 2>/dev/null
    return 1
  fi
  local auth_body auth_req
  auth_req="{\"user_code\":\"$cli_user\",\"approve\":true}"
  auth_body="$(http_body "$(curl -sS -w $'\n%{http_code}' -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $J8_SESSION_TOKEN" \
    -d "$auth_req" "$BASE/auth/device/authorize")")"
  if ! printf '%s' "$auth_body" | grep -q approved; then
    info "  DEBUG approve failed: cli_user=[$cli_user] req=[$auth_req] login.log:"
    tail -5 "$log" | sed 's/^/    /'
    fail J8-S1 "could not approve the CLI user_code ($auth_body)"
    kill "$login_pid" 2>/dev/null; wait "$login_pid" 2>/dev/null
    return 1
  fi
  for i in $(seq 1 30); do
    if ! kill -0 "$login_pid" 2>/dev/null; then wait "$login_pid"; login_rc=$?; break; fi
    sleep 1
  done
  if [ "$login_rc" != "0" ]; then
    fail J8-S1 "jcode login did not exit 0 (rc=$login_rc)"
    return 1
  fi
  J8_DEVICE_ID="$(jq -r '.device_id // empty' "$J8_HOME/.jcode/cloud.json" 2>/dev/null)"
  if [ -z "$J8_DEVICE_ID" ]; then
    fail J8-S1 "cloud.json missing device_id after login"
    return 1
  fi
  info "  device logged in: $J8_DEVICE_ID (real device-code flow)"

  # j8_start_web — (re)start `jcode web` on the temp HOME/workspace; waits for
  # /api/health. The subshell execs so $! is the jcode process itself.
  j8_start_web() {
    ( cd "$J8_WS" && exec env HOME="$J8_HOME" "$JCODE_BIN" web \
        --port "$J8_WEB_PORT" --host 127.0.0.1 --open=false \
        >>"$J8_HOME/web.log" 2>&1 ) &
    J8_WEB_PID=$!
    local j
    for j in $(seq 1 30); do
      if [ "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$J8_WEB_PORT/api/health" 2>/dev/null)" = "200" ]; then
        return 0
      fi
      kill -0 "$J8_WEB_PID" 2>/dev/null || return 1
      sleep 1
    done
    return 1
  }
  : >"$J8_HOME/web.log"
  if ! j8_start_web; then
    fail J8-S1 "jcode web never became healthy (log: $(tail -5 "$J8_HOME/web.log"))"
    return 1
  fi
  info "  jcode web up on 127.0.0.1:$J8_WEB_PORT (pid $J8_WEB_PID, ws $J8_WS)"

  # --- J8-S1: device online + auth negatives --------------------------------
  local devs online="" dev_body
  for i in $(seq 1 60); do
    dev_body="$(http_body "$(j8_get_code "/api/v1/devices")")"
    online="$(printf '%s' "$dev_body" | jq -r --arg id "$J8_DEVICE_ID" \
      '.devices[]? | select(.id==$id) | .online' 2>/dev/null)"
    [ "$online" = "true" ] && break
    sleep 1
  done
  assert_eq J8-S1 "GET /api/v1/devices shows the device online=true after register+heartbeat" \
    "true" "$online"
  devs="$(printf '%s' "$dev_body" | jq -r --arg id "$J8_DEVICE_ID" \
    '.devices[]? | select(.id==$id) | .name' 2>/dev/null)"
  assert_eq J8-S1 "device name mirrors the login --name" "e2e-j8" "$devs"
  # Negatives: the service principal owns no devices; a stranger's/unknown id 404s.
  local resp code
  resp="$(curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/devices")"
  assert_eq J8-S1 "GET /api/v1/devices with CONSOLE_TOKEN returns 400 (user session required)" \
    "400" "$(http_code "$resp")"
  resp="$(j8_get_code "/api/v1/devices/ffffffffffffffffffffffffffffffff")"
  assert_eq J8-S1 "GET /api/v1/devices/{unknown} returns 404" "404" "$(http_code "$resp")"
  if [ "$online" != "true" ]; then
    fail J8-S2 "device never came online; cannot continue J8 (log: $(tail -5 "$J8_HOME/web.log"))"
    return 1
  fi

  # --- J8-S2: remote chat.send creates a session ----------------------------
  resp="$(j8_post_code "/api/v1/devices/$J8_DEVICE_ID/sessions/new/messages" '{"text":"say hi"}')"
  code="$(http_code "$resp")"
  assert_eq J8-S2 "POST sessions/new/messages returns 202" "202" "$code"
  local cmd_id
  cmd_id="$(printf '%s' "$(http_body "$resp")" | jq -r '.command_id // empty')"
  assert_nonempty J8-S2 "202 carries a command_id" "$cmd_id"

  local sid="" sstatus="" saw_running="false" sbody
  for i in $(seq 1 90); do
    sbody="$(http_body "$(j8_get_code "/api/v1/devices/$J8_DEVICE_ID/sessions")")"
    sid="$(printf '%s' "$sbody" | jq -r '.sessions[0].session_id // empty' 2>/dev/null)"
    if [ -n "$sid" ]; then
      sstatus="$(printf '%s' "$sbody" | jq -r --arg s "$sid" \
        '.sessions[] | select(.session_id==$s) | .status' 2>/dev/null)"
      [ "$sstatus" = "running" ] && saw_running="true"
      [ "$sstatus" = "idle" ] && break
    fi
    sleep 1
  done
  assert_nonempty J8-S2 "session index gains a session after chat.send" "$sid"
  assert_eq J8-S2 "session settles to idle" "idle" "$sstatus"
  info "  cloud session $sid (running observed during run: $saw_running)"
  if [ -n "$sid" ]; then
    local local_sessions
    local_sessions="$(curl -sS "http://127.0.0.1:$J8_WEB_PORT/api/sessions")"
    assert_contains J8-S2 "session exists in the LOCAL jcode web (/api/sessions)" \
      "$local_sessions" "$sid"
  fi
  if [ -z "$sid" ]; then
    fail J8-S3 "no session mirrored; cannot continue J8 (log: $(tail -5 "$J8_HOME/web.log"))"
    return 1
  fi

  # --- J8-S3: durable event replay ------------------------------------------
  local evs ev_body kinds seqs_ok
  ev_body="$(http_body "$(j8_get_code "/api/v1/devices/$J8_DEVICE_ID/sessions/$sid/events?after_seq=0&limit=1000")")"
  evs="$(printf '%s' "$ev_body" | jq -r '.events | length' 2>/dev/null)"
  if [ "${evs:-0}" -gt 0 ] 2>/dev/null; then
    pass J8-S3 "events replay returns $evs durable events"
  else
    fail J8-S3 "events replay is empty (body: $(printf '%.200s' "$ev_body"))"
  fi
  seqs_ok="$(printf '%s' "$ev_body" | jq '([.events[].seq]) as $s | ($s|length) as $n | ($n > 0) and ($s == [range(1;$n+1)])' 2>/dev/null)"
  assert_eq J8-S3 "durable seqs are 1..N gapless and ascending" "true" "$seqs_ok"
  kinds="$(printf '%s' "$ev_body" | jq -r '.events[].kind' 2>/dev/null)"
  for k in user_message tool_call tool_result agent_done; do
    assert_contains J8-S3 "durable log carries a $k event" "$kinds" "$k"
  done
  local chan
  chan="$(printf '%s' "$ev_body" | jq -r '[.events[] | select(.kind=="user_message") | .payload.data.source] | unique | join(",")' 2>/dev/null)"
  assert_eq J8-S3 "user_message payload marks the channel source" "console" "$chan"

  # --- J8-S4: live SSE stream ------------------------------------------------
  local stream_out="$J8_HOME/stream.out" spid sse_ok="false"
  : >"$stream_out"
  curl -sS -N --max-time 45 -H "Authorization: Bearer $J8_SESSION_TOKEN" \
    "$BASE/api/v1/devices/$J8_DEVICE_ID/stream" >"$stream_out" 2>/dev/null &
  spid=$!
  sleep 1 # let the stream connect and deliver the initial device.status frame
  resp="$(j8_post_code "/api/v1/devices/$J8_DEVICE_ID/sessions/$sid/messages" '{"text":"say hi again"}')"
  assert_eq J8-S4 "POST a second message returns 202" "202" "$(http_code "$resp")"
  for i in $(seq 1 30); do
    if grep -q 'event: session.event' "$stream_out" 2>/dev/null; then sse_ok="true"; break; fi
    sleep 1
  done
  assert_eq J8-S4 "live stream carries session.event frames for the new message" "true" "$sse_ok"
  assert_contains J8-S4 "stream opens with the current device.status(online) frame" \
    "$(cat "$stream_out")" "device.status"
  if grep -q 'event: session.delta' "$stream_out" 2>/dev/null; then
    pass J8-S4 "stream carries session.delta (ephemeral agent_text) frames"
  else
    fail J8-S4 "no session.delta frames observed (ephemeral path)"
  fi
  kill "$spid" 2>/dev/null; wait "$spid" 2>/dev/null
  # Let the second run finish before the stop/offline steps.
  local s2status=""
  for i in $(seq 1 60); do
    s2status="$(http_body "$(j8_get_code "/api/v1/devices/$J8_DEVICE_ID/sessions")" \
      | jq -r --arg s "$sid" '.sessions[] | select(.session_id==$s) | .status' 2>/dev/null)"
    [ "$s2status" = "idle" ] && break
    sleep 1
  done
  info "  second run settled: $s2status"

  # --- J8-S5: chat.stop + approval.respond -----------------------------------
  # The session is idle by now, so the stop lands on a finished run; the
  # stable contract asserted here is queue→deliver→execute→ack, not the race
  # of catching a live run (the local /api/stop answers not_running, the ack
  # is still "ok" -> device_commands.acked).
  resp="$(j8_post_code "/api/v1/devices/$J8_DEVICE_ID/sessions/$sid/stop" '{}')"
  code="$(http_code "$resp")"
  assert_eq J8-S5 "POST stop returns 202" "202" "$code"
  local stop_cmd stop_st stop_result
  stop_cmd="$(printf '%s' "$(http_body "$resp")" | jq -r '.command_id // empty')"
  stop_st="$(j8_wait_command "$stop_cmd")"
  assert_eq J8-S5 "chat.stop command is acked by the device" "acked" "$stop_st"
  stop_result="$(j8_psql "SELECT convert_from(result,'UTF8') FROM device_commands WHERE id='$stop_cmd'" 2>/dev/null)"
  info "  chat.stop ack result: $(printf '%.120s' "$stop_result")"

  resp="$(j8_post_code "/api/v1/devices/$J8_DEVICE_ID/sessions/$sid/approval" \
    '{"approval_id":"e2e-bogus-approval","decision":"approve"}')"
  code="$(http_code "$resp")"
  assert_eq J8-S5 "POST approval returns 202" "202" "$code"
  local ap_cmd ap_st
  ap_cmd="$(printf '%s' "$(http_body "$resp")" | jq -r '.command_id // empty')"
  ap_st="$(j8_wait_command "$ap_cmd")"
  # Bogus approval_id -> local control plane 404 -> connector acks "error" ->
  # the command lands in `failed`. Terminal either way proves the loop.
  if [ "$ap_st" = "acked" ] || [ "$ap_st" = "failed" ]; then
    pass J8-S5 "approval.respond command reaches a terminal state ($ap_st; bogus id -> local 404)"
  else
    fail J8-S5 "approval.respond command stuck (status='$ap_st')"
  fi

  # --- J8-S6: offline transition + 409 device_offline -------------------------
  local prev_max
  prev_max="$(http_body "$(j8_get_code "/api/v1/devices/$J8_DEVICE_ID/sessions/$sid/events?after_seq=0&limit=1000")" \
    | jq -r '[.events[].seq] | max // 0' 2>/dev/null)"
  kill "$J8_WEB_PID" 2>/dev/null; wait "$J8_WEB_PID" 2>/dev/null
  J8_WEB_PID=""
  info "  jcode web killed; waiting for the heartbeat TTL (90s) to expire"
  local off="false" draw=""
  for i in $(seq 1 150); do
    draw="$(j8_get_code "/api/v1/devices/$J8_DEVICE_ID")"
    off="$(http_body "$draw" | jq -r 'if .online == null then "" else .online end' 2>/dev/null)"
    [ "$off" = "false" ] && break
    sleep 1
  done
  if [ "$off" != "false" ]; then
    info "  DEBUG offline poll: last raw response (code=$(http_code "$draw")): $(printf '%.300s' "$(http_body "$draw")")"
  fi
  assert_eq J8-S6 "device flips online=false after the heartbeat TTL" "false" "$off"
  resp="$(j8_post_code "/api/v1/devices/$J8_DEVICE_ID/sessions/$sid/messages" '{"text":"still there?"}')"
  assert_eq J8-S6 "POST messages to an offline device returns 409" "409" "$(http_code "$resp")"
  assert_contains J8-S6 "409 error is device_offline" "$(http_body "$resp")" "device_offline"
  resp="$(j8_post_code "/api/v1/devices/$J8_DEVICE_ID/sessions/$sid/stop" '{}')"
  assert_eq J8-S6 "POST stop to an offline device returns 409" "409" "$(http_code "$resp")"

  # --- J8-S7: reconnect + seq continuation ------------------------------------
  if ! j8_start_web; then
    fail J8-S7 "jcode web restart never became healthy (log: $(tail -5 "$J8_HOME/web.log"))"
    return 1
  fi
  local on="false"
  for i in $(seq 1 60); do
    on="$(http_body "$(j8_get_code "/api/v1/devices/$J8_DEVICE_ID")" | jq -r 'if .online == null then "" else .online end' 2>/dev/null)"
    [ "$on" = "true" ] && break
    sleep 1
  done
  assert_eq J8-S7 "device is online again after the connector restarts" "true" "$on"
  resp="$(j8_post_code "/api/v1/devices/$J8_DEVICE_ID/sessions/$sid/messages" '{"text":"back online"}')"
  assert_eq J8-S7 "POST messages after reconnect returns 202" "202" "$(http_code "$resp")"
  local new_max="0" gapless="false"
  for i in $(seq 1 90); do
    ev_body="$(http_body "$(j8_get_code "/api/v1/devices/$J8_DEVICE_ID/sessions/$sid/events?after_seq=0&limit=2000")")"
    new_max="$(printf '%s' "$ev_body" | jq -r '[.events[].seq] | max // 0' 2>/dev/null)"
    if [ "${new_max:-0}" -gt "${prev_max:-0}" ] 2>/dev/null; then
      gapless="$(printf '%s' "$ev_body" | jq '([.events[].seq]) as $s | ($s|length) as $n | ($s == [range(1;$n+1)])' 2>/dev/null)"
      [ "$gapless" = "true" ] && break
    fi
    sleep 1
  done
  if [ "${new_max:-0}" -gt "${prev_max:-0}" ] 2>/dev/null; then
    pass J8-S7 "durable seqs continue past the pre-restart max ($prev_max -> $new_max, server-seeded)"
  else
    fail J8-S7 "no new durable events after reconnect (max seq $new_max <= $prev_max)"
  fi
  assert_eq J8-S7 "full event log is still 1..N gapless after reconnect" "true" "$gapless"
}

# Standalone execution.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  trap j8_cleanup EXIT
  j8_run
  rc=$?
  j8_cleanup
  print_summary 2>/dev/null || exit 1
  exit "$rc"
fi
