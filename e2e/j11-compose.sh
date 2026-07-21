#!/usr/bin/env bash
# j11-compose.sh — Journey J11 "compose 五要素" (design contract: cloud/docs/
# 17-jcode-device-relay.md §4/§6, module M12, wire contract
# jcode-cloud-relay/modules/M12-compose.md). Drives the M12 compose loop
# against the live orchestrator with a REAL `jcode web` process (temp HOME,
# mockllm as the model), logged in via the J10 visual (web control plane)
# flow — no CLI. A node WebCrypto client (j9-client.mjs) pairs via the QR
# offer flow, then sends an encrypted chat.send carrying all five compose
# facets (project_path / model / effort / goal / attachments) and verifies
# them server-side, on-device, and in the durable replay.
#
# Asserts:
#   J11-S1  capabilities mirror: GET /api/v1/devices/{id} eventually carries a
#           sealed `capabilities` envelope; CEK-decrypted it lists the two
#           pre-created project dirs (projects), the mockllm model (models),
#           and a non-empty efforts list
#   J11-S2  compose chat.send: encrypted {text, project_path, model, effort,
#           goal, attachments} -> 202 + command_id; the command is acked and
#           the new session (in /tmp/j11-proj-b) settles to idle
#   J11-S3  device-side effects: ~/.jcode/inbox/<sid>/note.txt exists with the
#           attachment bytes and 0600 perms; the session index keys the
#           session under /tmp/j11-proj-b; the CEK-decrypted durable replay
#           carries goal_update (objective) and user_message (text with the
#           "[附件] note.txt →" reference); model_state.json records the
#           effort override and /api/health shows the switched model
#   J11-S4  zero plaintext at rest: psql over device_events/device_commands
#           for the session shows neither "hello-attachment" nor the goal text
#   J11-S5  negatives: an attachment over the 2MB limit -> RECORDED M12
#           finding: the ~2.9MB sealed command exceeds the connector's 1MB
#           poll-response read cap, so the command is orphaned in `delivered`
#           (no ack, no file on disk, queue unharmed); a nonexistent
#           project_path -> the local control plane mkdirs it and the command
#           acks `acked` (asserted + recorded as the actual semantics)
#   J11-S6  M20 mode ceiling: mode=full_access chat.send -> command acks
#           `failed`, CEK-decrypted ack result carries
#           mode_not_allowed_for_cloud; mode=auto still acks `acked`
#
# Sourced by e2e.sh (BASE/TOKEN exported) OR runnable standalone:
#   BASE=http://127.0.0.1:18080 ./j11-compose.sh
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

: "${JCODE_BIN:=$HERE/../../jcode/jcode}"
: "${J11_MOCK_PORT:=18084}"   # scratch port-forward to svc/mockllm:8081
: "${J11_WEB_PORT:=18089}"    # local `jcode web` bind (loopback only)
J11_NODE_CLIENT="$HERE/j9-client.mjs"

J11_USER_ID="e2edevice0000000000000000000000n1"
J11_SESSION_ID="e2edevice0000000000000000000000n2"
J11_SESSION_TOKEN="e2e-compose-session-token"
J11_PROJ_A="/tmp/j11-proj-a"
J11_PROJ_B="/tmp/j11-proj-b"
J11_PROJ_MISSING="/tmp/j11-no-such-project"
J11_GOAL="j11 e2e goal"
J11_ATT_CONTENT="hello-attachment"
J11_ATT_B64="aGVsbG8tYXR0YWNobWVudA==" # base64("hello-attachment")

# Run-scoped state (also used by j11_cleanup from e2e.sh's teardown trap).
J11_HOME=""; J11_WS=""; J11_WEB_PID=""; J11_MOCK_PF_PID=""; J11_DEVICE_ID=""

j11_psql() {
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -tAc "$1"
}

j11_seed_session() {
  local hash
  hash="$(printf '%s' "$J11_SESSION_TOKEN" | shasum -a 256 | awk '{print $1}')"
  j11_psql "INSERT INTO users (id, display_name, avatar_url, is_cluster_admin, created_at)
             VALUES ('$J11_USER_ID', 'e2e-compose-tester', '', false, now())
             ON CONFLICT (id) DO NOTHING;
           INSERT INTO sessions (id, user_id, token_hash, created_at, expires_at, revoked_at)
             VALUES ('$J11_SESSION_ID', '$J11_USER_ID', '$hash', now(), now() + interval '1 day', NULL)
             ON CONFLICT (id) DO UPDATE
               SET token_hash=EXCLUDED.token_hash, expires_at=EXCLUDED.expires_at, revoked_at=NULL;" \
    >/dev/null
}

# j11_cleanup stops the local processes (jcode web, mockllm port-forward),
# drops the throwaway HOME/workspace/project dirs and the seeded user (ON
# DELETE CASCADE removes sessions, devices, device_tokens, device_sessions,
# device_events, device_commands, device_pairings, device_pairing_offers).
j11_cleanup() {
  [ -n "$J11_WEB_PID" ] && kill "$J11_WEB_PID" 2>/dev/null
  [ -n "$J11_MOCK_PF_PID" ] && kill "$J11_MOCK_PF_PID" 2>/dev/null
  [ -n "$J11_HOME" ] && rm -rf "$J11_HOME"
  [ -n "$J11_WS" ] && rm -rf "$J11_WS"
  rm -rf "$J11_PROJ_A" "$J11_PROJ_B" "$J11_PROJ_MISSING"
  J11_WEB_PID=""; J11_MOCK_PF_PID=""; J11_HOME=""; J11_WS=""
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -c "DELETE FROM users WHERE id='$J11_USER_ID'" >/dev/null 2>&1 || true
}

# User-session-authenticated helpers (the client API requires a real user
# session, not the CONSOLE_TOKEN service principal).
j11_get_code() {
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $J11_SESSION_TOKEN" "$BASE$1"
}
j11_post_code() {
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $J11_SESSION_TOKEN" \
    -H 'Content-Type: application/json' -d "$2" "$BASE$1"
}
# Local web control plane helpers (loopback, no auth — same as j10).
j11_local_get() { curl -sS "http://127.0.0.1:$J11_WEB_PORT$1"; }
j11_local_post_code() {
  curl -sS -w $'\n%{http_code}' -H 'Content-Type: application/json' \
    -d "$2" "http://127.0.0.1:$J11_WEB_PORT$1"
}

# j11_wait_command <command_id> -> polls device_commands until the command
# leaves pending/delivered; prints the terminal status (acked|failed|…).
j11_wait_command() {
  local cid="$1" st="" i
  for i in $(seq 1 60); do
    st="$(j11_psql "SELECT status FROM device_commands WHERE id='$cid'" 2>/dev/null | tr -d '[:space:]')"
    case "$st" in acked|failed|canceled) printf '%s' "$st"; return 0;; esac
    sleep 1
  done
  printf '%s' "$st"; return 1
}

# j11_seal_send <plaintext-file> -> seals the chat.send payload with the CEK
# and POSTs it as {envelope} to sessions/new/messages; prints the raw
# "body\ncode" response. The request body goes through a FILE (a >2MB
# attachment seals into a multi-MB envelope that would blow the exec argv
# limit if passed via curl -d).
j11_seal_send() {
  node "$J11_NODE_CLIENT" seal "$J11_HOME/cek.json" "$1" >"$J11_HOME/env.json" 2>>"$J11_HOME/node.log"
  printf '{"envelope":%s}' "$(cat "$J11_HOME/env.json")" >"$J11_HOME/req.json"
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $J11_SESSION_TOKEN" \
    -H 'Content-Type: application/json' --data-binary @"$J11_HOME/req.json" \
    "$BASE/api/v1/devices/$J11_DEVICE_ID/sessions/new/messages"
}

# j11_open_result <command_id> -> prints the CEK-decrypted ack result of a
# device command (the connector seals ack results when E2EE is active).
j11_open_result() {
  j11_psql "SELECT convert_from(result,'UTF8') FROM device_commands WHERE id='$1'" 2>/dev/null \
    >"$J11_HOME/ack-env.json"
  node "$J11_NODE_CLIENT" open "$J11_HOME/cek.json" "$J11_HOME/ack-env.json" 2>>"$J11_HOME/node.log"
}

j11_run() {
  section "J11 · compose 五要素 (docs/17 §4/§6 — M12, real jcode web + mockllm, visual login, WebCrypto client)"

  if [ ! -x "$JCODE_BIN" ]; then
    skip J11-S1 "jcode binary not found at $JCODE_BIN (build: make -C ../jcode build-binary)"
    return 0
  fi
  if ! command -v node >/dev/null 2>&1; then
    skip J11-S1 "node not found (J11 needs node:crypto webcrypto for the client side)"
    return 0
  fi

  # --- setup: seed, port-forward mockllm, temp HOME (NO CLI login) ----------
  j11_cleanup
  if ! j11_seed_session; then
    fail J11-S1 "could not seed the e2e-compose-tester user/session into Postgres"
    return 1
  fi
  local me_code
  me_code="$(http_code "$(j11_get_code "/api/v1/me")")"
  if [ "$me_code" != "200" ]; then
    fail J11-S1 "seeded session does not resolve (GET /api/v1/me -> $me_code)"
    return 1
  fi

  kubectl --context "$KCTX" -n "$NAMESPACE" port-forward svc/mockllm \
    "$J11_MOCK_PORT:8081" >/tmp/j11-mockllm-pf.log 2>&1 &
  J11_MOCK_PF_PID=$!
  local mock_ready="false" i
  for i in $(seq 1 20); do
    if curl -sS -o /dev/null "http://127.0.0.1:$J11_MOCK_PORT/health" 2>/dev/null; then
      mock_ready="true"; break
    fi
    sleep 1
  done
  if [ "$mock_ready" != "true" ]; then
    fail J11-S1 "mockllm port-forward never became healthy (log: $(tail -2 /tmp/j11-mockllm-pf.log))"
    return 1
  fi
  info "  mockllm reachable at 127.0.0.1:$J11_MOCK_PORT"

  J11_HOME="$(mktemp -t j11-home.XXXXXX)"; rm -f "$J11_HOME"; mkdir -p "$J11_HOME/.jcode"
  J11_WS="$(mktemp -t j11-ws.XXXXXX)"; rm -f "$J11_WS"; mkdir -p "$J11_WS"
  # No cloud.e2ee key: the default (true) is what J11 exercises — capabilities
  # and event/command payloads all travel as sealed envelopes.
  cat >"$J11_HOME/.jcode/config.json" <<JSON
{
  "providers": {
    "mock": {
      "api_key": "dummy-key",
      "base_url": "http://127.0.0.1:$J11_MOCK_PORT/v1",
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

  ( cd "$J11_WS" && exec env HOME="$J11_HOME" "$JCODE_BIN" web \
      --port "$J11_WEB_PORT" --host 127.0.0.1 --open=false \
      >>"$J11_HOME/web.log" 2>&1 ) &
  J11_WEB_PID=$!
  local web_ready="false"
  for i in $(seq 1 30); do
    if [ "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$J11_WEB_PORT/api/health" 2>/dev/null)" = "200" ]; then
      web_ready="true"; break
    fi
    kill -0 "$J11_WEB_PID" 2>/dev/null || break
    sleep 1
  done
  if [ "$web_ready" != "true" ]; then
    fail J11-S1 "jcode web never became healthy (log: $(tail -5 "$J11_HOME/web.log"))"
    return 1
  fi
  info "  jcode web up on 127.0.0.1:$J11_WEB_PORT (pid $J11_WEB_PID, fresh HOME — not logged in)"

  # --- visual login (J10 pattern: device-code via the web backend, no CLI) --
  local resp code body user_code
  resp="$(j11_local_post_code "/api/cloud/login" "{\"cloud_url\":\"$BASE\"}")"
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  assert_eq J11-S1 "POST /api/cloud/login returns 200" "200" "$code"
  user_code="$(printf '%s' "$body" | jq -r '.user_code // empty')"
  if [ -z "$user_code" ]; then
    fail J11-S1 "no user_code; cannot continue J11 (body: $(printf '%.200s' "$body"))"
    return 1
  fi
  local auth_body auth_resp
  auth_resp="$(curl -sS -w $'\n%{http_code}' -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $J11_SESSION_TOKEN" \
    -d "{\"user_code\":\"$user_code\",\"approve\":true}" "$BASE/auth/device/authorize")"
  auth_body="$(http_body "$auth_resp")"
  info "  authorize user_code=$user_code -> $(http_code "$auth_resp") $(printf '%.200s' "$auth_body")"
  assert_contains J11-S1 "seeded session approves the user_code" "$auth_body" "approved"

  local state=""
  for i in $(seq 1 90); do
    state="$(j11_local_get "/api/cloud/login/status" | jq -r '.state // empty' 2>/dev/null)"
    [ "$state" = "success" ] && break
    sleep 1
  done
  assert_eq J11-S1 "GET /api/cloud/login/status reaches success" "success" "$state"

  J11_DEVICE_ID="$(jq -r '.device_id // empty' "$J11_HOME/.jcode/cloud.json" 2>/dev/null)"
  if [ -z "$J11_DEVICE_ID" ]; then
    fail J11-S1 "cloud.json missing device_id after login (log: $(tail -5 "$J11_HOME/web.log"))"
    return 1
  fi
  info "  device logged in: $J11_DEVICE_ID (visual login)"

  local online=""
  for i in $(seq 1 60); do
    online="$(http_body "$(j11_get_code "/api/v1/devices/$J11_DEVICE_ID")" \
      | jq -r 'if .online == null then "" else .online end' 2>/dev/null)"
    [ "$online" = "true" ] && break
    sleep 1
  done
  assert_eq J11-S1 "connector online after the web login" "true" "$online"
  if [ "$online" != "true" ]; then
    fail J11-S1 "device never came online; cannot continue J11 (log: $(tail -5 "$J11_HOME/web.log"))"
    return 1
  fi

  # --- pre-create two project dirs, each with a local session ---------------
  # capabilities.projects mirrors the local session index (keyed by project
  # path). The index entry is written on the session's FIRST recorded content
  # (Recorder indexing requires content), so each project gets one session
  # plus one cheap mockllm message — POST /api/sessions {pwd} alone leaves
  # the index empty.
  mkdir -p "$J11_PROJ_A" "$J11_PROJ_B"
  local seed_sid seed_idx="$J11_HOME/.jcode/sessions/session.json"
  for proj in "$J11_PROJ_A" "$J11_PROJ_B"; do
    resp="$(j11_local_post_code "/api/sessions" "{\"pwd\":\"$proj\"}")"
    assert_eq J11-S1 "local session created in $proj (POST /api/sessions {pwd})" "200" "$(http_code "$resp")"
    seed_sid="$(printf '%s' "$(http_body "$resp")" | jq -r '.session_id // .uuid // empty')"
    resp="$(j11_local_post_code "/api/chat" "{\"message\":\"seed\",\"session_id\":\"$seed_sid\"}")"
    code="$(http_code "$resp")"
    assert_true J11-S1 "seed message accepted in $proj (indexes the session)" \
      "$([ "$code" = "200" ] || [ "$code" = "202" ] && echo true || echo false)"
    for i in $(seq 1 30); do
      jq -e --arg p "$proj" '.sessions[$p] and (.sessions[$p] | length > 0)' "$seed_idx" >/dev/null 2>&1 && break
      sleep 1
    done
    assert_eq J11-S1 "session index lists $proj" "true" \
      "$(jq -r --arg p "$proj" '(.sessions[$p] // []) | length > 0' "$seed_idx" 2>/dev/null)"
  done

  # --- pair a node client via the QR offer flow (J10 pattern) ---------------
  local offer_id secret qr
  resp="$(j11_local_post_code "/api/cloud/pairing-offer" '{}')"
  qr="$(printf '%s' "$(http_body "$resp")" | jq -r '.qr // empty')"
  offer_id="$(printf '%s' "$(http_body "$resp")" | jq -r '.offer_id // empty')"
  secret="$(printf '%s' "${qr#jcode://pair?}" | tr '&' '\n' | sed -n 's/^secret=//p')"
  if [ -z "$offer_id" ] || [ -z "$secret" ]; then
    fail J11-S1 "could not mint a pairing offer (body: $(printf '%.200s' "$(http_body "$resp")"))"
    return 1
  fi
  node "$J11_NODE_CLIENT" keygen "$J11_HOME/client-priv.b64" >"$J11_HOME/client-pub.b64" 2>"$J11_HOME/node.log" || {
    fail J11-S1 "node keygen failed ($(cat "$J11_HOME/node.log"))"
    return 1
  }
  resp="$(j11_post_code "/api/v1/pairing-offers/$offer_id/claim" \
    "{\"secret\":\"$secret\",\"label\":\"e2e-compose-client\",\"kty\":\"P-256\",\"pubkey\":\"$(cat "$J11_HOME/client-pub.b64")\"}")"
  local pid
  pid="$(printf '%s' "$(http_body "$resp")" | jq -r '.pairing_id // empty')"
  if [ -z "$pid" ]; then
    fail J11-S1 "offer claim returned no pairing_id (body: $(printf '%.200s' "$(http_body "$resp")"))"
    return 1
  fi
  local pstatus="" pview
  for i in $(seq 1 90); do
    pview="$(http_body "$(j11_get_code "/api/v1/devices/$J11_DEVICE_ID/pairings/$pid")")"
    pstatus="$(printf '%s' "$pview" | jq -r '.status // empty')"
    [ "$pstatus" = "approved" ] && break
    sleep 1
  done
  assert_eq J11-S1 "QR pairing auto-approves (offer claim, no approve action)" "approved" "$pstatus"
  printf '%s' "$pview" | jq -r '.wrap // empty' >"$J11_HOME/wrap.json"
  local key_gen
  key_gen="$(node "$J11_NODE_CLIENT" unwrap "$J11_HOME/wrap.json" "$J11_HOME/client-priv.b64" "$J11_HOME/cek.json" 2>>"$J11_HOME/node.log")"
  assert_eq J11-S1 "client unwraps the CEK (key_gen=1)" "1" "$key_gen"
  if [ ! -s "$J11_HOME/cek.json" ]; then
    fail J11-S1 "no CEK; cannot continue J11"
    return 1
  fi

  # --- J11-S1: capabilities mirror -------------------------------------------
  # The connector seals the capabilities JSON like session meta and rides it
  # on the sessions upsert; the orchestrator stores it opaquely in
  # devices.capabilities and echoes it in the device view. Poll (the upsert
  # re-fires when the local session index changes) until the decrypted
  # projects list covers both pre-created dirs.
  local caps="" caps_ok="false"
  for i in $(seq 1 60); do
    body="$(http_body "$(j11_get_code "/api/v1/devices/$J11_DEVICE_ID")")"
    if printf '%s' "$body" | jq -e '.capabilities' >/dev/null 2>&1; then
      printf '%s' "$body" | jq '.capabilities' >"$J11_HOME/caps-env.json"
      caps="$(node "$J11_NODE_CLIENT" open "$J11_HOME/cek.json" "$J11_HOME/caps-env.json" 2>>"$J11_HOME/node.log")"
      if printf '%s' "$caps" | jq -e --arg a "$J11_PROJ_A" --arg b "$J11_PROJ_B" \
        '([.projects[]?.path] | index($a) != null) and ([.projects[]?.path] | index($b) != null)' \
        >/dev/null 2>&1; then
        caps_ok="true"; break
      fi
    fi
    sleep 1
  done
  assert_eq J11-S1 "device view carries capabilities; decrypted projects cover both pre-created dirs" \
    "true" "$caps_ok"
  if [ "$caps_ok" = "true" ]; then
    info "  decrypted capabilities: $(printf '%s' "$caps" | jq -c '{projects:[.projects[].path], models, efforts}')"
    local caps_models caps_efforts
    caps_models="$(printf '%s' "$caps" | jq -r '[.models[]? | .provider + "/" + .id] | join(",")')"
    assert_contains J11-S1 "capabilities.models advertises the mockllm model" "$caps_models" "mock/mock-model"
    caps_efforts="$(printf '%s' "$caps" | jq -r '[.efforts[]?] | join(",")')"
    assert_contains J11-S1 "capabilities.efforts is non-empty and offers low" "$caps_efforts" "low"
    # Zero-plaintext spot check on the mirror itself: the stored capabilities
    # are a sealed envelope (project paths must not appear at rest).
    local caps_row
    caps_row="$(j11_psql "SELECT capabilities::text FROM devices WHERE id='$J11_DEVICE_ID'" 2>/dev/null)"
    assert_contains J11-S1 "devices.capabilities at rest is a sealed envelope" "$caps_row" 'aes-256-gcm'
    assert_not_contains J11-S1 "no project path plaintext at rest (devices.capabilities)" "$caps_row" "j11-proj-b"
  else
    fail J11-S2 "capabilities never mirrored; cannot continue J11 (last: $(printf '%.200s' "$caps"))"
    return 1
  fi

  # --- J11-S2: compose chat.send (all five facets) ----------------------------
  cat >"$J11_HOME/plain.json" <<JSON
{
  "text": "reply pong",
  "channel": "console",
  "project_path": "$J11_PROJ_B",
  "model": {"provider": "mock", "id": "mock-model"},
  "effort": "low",
  "goal": "$J11_GOAL",
  "attachments": [{"name": "note.txt", "mime": "text/plain", "data_b64": "$J11_ATT_B64"}]
}
JSON
  resp="$(j11_seal_send "$J11_HOME/plain.json")"
  code="$(http_code "$resp")"
  assert_eq J11-S2 "compose chat.send {envelope} returns 202" "202" "$code"
  local cmd_id cmd_st sid
  cmd_id="$(printf '%s' "$(http_body "$resp")" | jq -r '.command_id // empty')"
  assert_nonempty J11-S2 "202 carries a command_id" "$cmd_id"
  if [ -z "$cmd_id" ]; then
    fail J11-S2 "no command_id; cannot continue J11"
    return 1
  fi
  cmd_st="$(j11_wait_command "$cmd_id")"
  assert_eq J11-S2 "compose chat.send command is acked by the device" "acked" "$cmd_st"
  # The ack result is a sealed envelope under E2EE — decrypt it for the sid.
  sid="$(j11_open_result "$cmd_id" | jq -r '.session_id // empty' 2>/dev/null)"
  assert_nonempty J11-S2 "ack result carries the new session_id (CEK-decrypted)" "$sid"
  if [ -z "$sid" ]; then
    fail J11-S2 "no session_id in the ack result; cannot continue J11 (log: $(tail -5 "$J11_HOME/web.log"))"
    return 1
  fi
  info "  compose session $sid (command $cmd_id $cmd_st)"

  local sstatus="" sbody
  for i in $(seq 1 90); do
    sbody="$(http_body "$(j11_get_code "/api/v1/devices/$J11_DEVICE_ID/sessions")")"
    sstatus="$(printf '%s' "$sbody" | jq -r --arg s "$sid" \
      '.sessions[]? | select(.session_id==$s) | .status' 2>/dev/null)"
    [ "$sstatus" = "idle" ] && break
    sleep 1
  done
  assert_eq J11-S2 "compose session settles to idle" "idle" "$sstatus"

  # --- J11-S3: device-side effects --------------------------------------------
  # Attachment landed in the per-session inbox.
  local att="$J11_HOME/.jcode/inbox/$sid/note.txt"
  if [ -f "$att" ]; then
    pass J11-S3 "inbox attachment exists (~/.jcode/inbox/<sid>/note.txt)"
  else
    fail J11-S3 "inbox attachment missing ($att; inbox: $(ls "$J11_HOME/.jcode/inbox" 2>/dev/null | tr '\n' ' '))"
  fi
  assert_eq J11-S3 "inbox attachment content is the decoded bytes" \
    "$J11_ATT_CONTENT" "$(cat "$att" 2>/dev/null)"
  assert_eq J11-S3 "inbox attachment permissions are 0600" "600" "$(stat -f '%Lp' "$att" 2>/dev/null)"

  # Session belongs to project B (local session index keyed by project path).
  local idx="$J11_HOME/.jcode/sessions/session.json"
  assert_eq J11-S3 "session index keys the compose session under project B" "true" \
    "$(jq -r --arg p "$J11_PROJ_B" --arg s "$sid" \
      '(.sessions[$p] // []) | map(.uuid) | index($s) != null' "$idx" 2>/dev/null)"

  # Durable replay (CEK-decrypted): goal_update carries the objective,
  # user_message carries the attachment reference line.
  local ev_body="" goal_plain="" user_plain="" line kind p64
  for i in $(seq 1 30); do
    ev_body="$(http_body "$(j11_get_code "/api/v1/devices/$J11_DEVICE_ID/sessions/$sid/events?after_seq=0&limit=1000")")"
    printf '%s' "$ev_body" | jq -e '[.events[]? | select(.kind=="goal_update")] | length > 0' >/dev/null 2>&1 && break
    sleep 1
  done
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    kind="${line%% *}"
    p64="${line#* }"
    printf '%s' "$p64" | base64 -d >"$J11_HOME/ev.json"
    case "$kind" in
      goal_update)  goal_plain="$goal_plain
$(node "$J11_NODE_CLIENT" open "$J11_HOME/cek.json" "$J11_HOME/ev.json" 2>>"$J11_HOME/node.log")";;
      user_message) user_plain="$user_plain
$(node "$J11_NODE_CLIENT" open "$J11_HOME/cek.json" "$J11_HOME/ev.json" 2>>"$J11_HOME/node.log")";;
    esac
  done <<EOF
$(printf '%s' "$ev_body" | jq -r '.events[] | select(.kind=="goal_update" or .kind=="user_message") | .kind + " " + (.payload | @base64)')
EOF
  assert_contains J11-S3 "decrypted goal_update carries the objective" "$goal_plain" "$J11_GOAL"
  info "  decrypted user_message events: $(printf '%s' "$user_plain" | head -c 400)"
  # NB: assert_contains needles are grep BREs — "[附件]" would parse as a
  # character class, so assert the two regex-safe halves instead.
  assert_contains J11-S3 "decrypted user_message carries the attachment marker" "$user_plain" "附件"
  assert_contains J11-S3 "decrypted user_message carries the attachment reference" \
    "$user_plain" "note.txt →"
  assert_contains J11-S3 "decrypted user_message marks data.source=console" "$user_plain" '"source":"console"'

  # Model + effort persisted on-device.
  assert_eq J11-S3 "model_state.json records effort_overrides[mock/mock-model]=low" "low" \
    "$(jq -r '.effort_overrides["mock/mock-model"] // empty' "$J11_HOME/.jcode/model_state.json" 2>/dev/null)"
  local health_pm
  health_pm="$(j11_local_get "/api/health" | jq -r '.provider + "/" + .model' 2>/dev/null)"
  assert_eq J11-S3 "active engine model switched via /api/model (GET /api/health)" "mock/mock-model" "$health_pm"

  # --- J11-S4: zero plaintext at rest -----------------------------------------
  local ev_rows cmd_env
  ev_rows="$(j11_psql "SELECT convert_from(envelope,'UTF8') FROM device_events WHERE session_id='$sid' ORDER BY seq" 2>/dev/null)"
  if [ -n "$ev_rows" ]; then
    pass J11-S4 "device_events holds rows for the compose session"
  else
    fail J11-S4 "device_events is empty for session $sid"
  fi
  assert_not_contains J11-S4 "no attachment plaintext at rest (device_events)" "$ev_rows" "$J11_ATT_CONTENT"
  assert_not_contains J11-S4 "no goal plaintext at rest (device_events)" "$ev_rows" "$J11_GOAL"
  cmd_env="$(j11_psql "SELECT convert_from(envelope,'UTF8') FROM device_commands WHERE id='$cmd_id'" 2>/dev/null)"
  assert_contains J11-S4 "the chat.send command payload is a sealed envelope" "$cmd_env" 'aes-256-gcm'
  assert_not_contains J11-S4 "no attachment plaintext at rest (device_commands)" "$cmd_env" "$J11_ATT_CONTENT"
  assert_not_contains J11-S4 "no goal plaintext at rest (device_commands)" "$cmd_env" "$J11_GOAL"

  # --- J11-S5: negatives --------------------------------------------------------
  # (a) attachment over the 2MB limit. RECORDED M12 LIMITATION (finding, not a
  # gate on "failed"): the sealed ~2.9MB command envelope exceeds the
  # connector transport's 1MB poll-response read cap (internal/cloud
  # client.go io.LimitReader 1<<20) — the poll body is truncated, the decode
  # fails, and the command is orphaned in `delivered` WITHOUT an ack. The
  # connector-side 2MB validation never runs, so the intended "acks failed"
  # path is unreachable at this size (any attachment >~750KB decoded trips
  # the transport cap first). Assert the actual semantics: never terminal
  # within the window, still delivered, no file on disk; the queue itself is
  # not wedged ((b) right after acks fine).
  node -e '
    const { writeFileSync } = require("fs");
    const big = Buffer.alloc(2 * 1024 * 1024 + 16, 65).toString("base64");
    writeFileSync(process.argv[1], JSON.stringify({
      text: "big attachment", channel: "console",
      attachments: [{ name: "big.bin", mime: "application/octet-stream", data_b64: big }],
    }));
  ' "$J11_HOME/plain-big.json"
  resp="$(j11_seal_send "$J11_HOME/plain-big.json")"
  code="$(http_code "$resp")"
  assert_eq J11-S5 "oversized-attachment chat.send still returns 202 (queued)" "202" "$code"
  local big_cmd big_st="" i
  big_cmd="$(printf '%s' "$(http_body "$resp")" | jq -r '.command_id // empty')"
  for i in $(seq 1 20); do
    big_st="$(j11_psql "SELECT status FROM device_commands WHERE id='$big_cmd'" 2>/dev/null | tr -d '[:space:]')"
    case "$big_st" in acked|failed|canceled) break;; esac
    sleep 1
  done
  assert_eq J11-S5 "oversized attachment: connector rejects with terminal ack=failed (transport cap raised to 32MB; the 2MB connector check now runs)" \
    "failed" "$big_st"
  assert_true J11-S5 "no oversized file landed in the inbox" \
    "$([ -z "$(find "$J11_HOME/.jcode/inbox" -name 'big.bin' 2>/dev/null)" ] && echo true || echo false)"

  # (b) nonexistent project_path: the local control plane does NOT reject it —
  # POST /api/sessions {pwd} creates the missing directory and the session
  # runs there (verified live: the dir appears with the mockllm artifact). The
  # command therefore acks ok; assert the actual semantics and record them.
  printf '{"text":"bad project","channel":"console","project_path":"%s"}' "$J11_PROJ_MISSING" \
    >"$J11_HOME/plain-badproj.json"
  resp="$(j11_seal_send "$J11_HOME/plain-badproj.json")"
  code="$(http_code "$resp")"
  assert_eq J11-S5 "nonexistent-project chat.send still returns 202 (queued)" "202" "$code"
  local bp_cmd bp_st
  bp_cmd="$(printf '%s' "$(http_body "$resp")" | jq -r '.command_id // empty')"
  bp_st="$(j11_wait_command "$bp_cmd")"
  assert_eq J11-S5 "nonexistent project_path -> acked (control plane mkdirs the missing dir; recorded behavior)" \
    "acked" "$bp_st"
  assert_true J11-S5 "the nonexistent project dir was created on-device" \
    "$([ -d "$J11_PROJ_MISSING" ] && echo true || echo false)"
  info "  nonexistent-project ack result (decrypted): $(j11_open_result "$bp_cmd" | head -c 200)"

  # --- J11-S6: M20 mode ceiling -------------------------------------------------
  # A cloud chat.send asking for full_access (bypass) is refused at the
  # protocol layer: the command acks `failed` and the (CEK-decrypted) ack
  # result carries mode_not_allowed_for_cloud. An allowed mode (auto) still
  # acks ok.
  printf '{"text":"try bypass","channel":"console","mode":"full_access","project_path":"%s"}' "$J11_PROJ_B" \
    >"$J11_HOME/plain-fa.json"
  resp="$(j11_seal_send "$J11_HOME/plain-fa.json")"
  code="$(http_code "$resp")"
  assert_eq J11-S6 "full_access chat.send still returns 202 (queued)" "202" "$code"
  local fa_cmd fa_st fa_result
  fa_cmd="$(printf '%s' "$(http_body "$resp")" | jq -r '.command_id // empty')"
  fa_st="$(j11_wait_command "$fa_cmd")"
  assert_eq J11-S6 "full_access chat.send command acks failed (M20 mode ceiling)" "failed" "$fa_st"
  fa_result="$(j11_open_result "$fa_cmd")"
  assert_contains J11-S6 "ack result carries mode_not_allowed_for_cloud" "$fa_result" "mode_not_allowed_for_cloud"
  info "  full_access ack result (decrypted): $(printf '%s' "$fa_result" | head -c 200)"

  printf '{"text":"auto is fine","channel":"console","mode":"auto","project_path":"%s"}' "$J11_PROJ_B" \
    >"$J11_HOME/plain-auto.json"
  resp="$(j11_seal_send "$J11_HOME/plain-auto.json")"
  local auto_cmd auto_st
  auto_cmd="$(printf '%s' "$(http_body "$resp")" | jq -r '.command_id // empty')"
  auto_st="$(j11_wait_command "$auto_cmd")"
  assert_eq J11-S6 "mode=auto chat.send still acks ok" "acked" "$auto_st"
}

# Standalone execution.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  trap j11_cleanup EXIT
  j11_run
  rc=$?
  j11_cleanup
  print_summary 2>/dev/null || exit 1
  exit "$rc"
fi
