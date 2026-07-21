#!/usr/bin/env bash
# j9-device-e2ee.sh — Journey J9 "device relay E2E encryption" (design
# contract: cloud/docs/17-jcode-device-relay.md §6, module M5). Same full relay
# loop as J8 (real `jcode web`, temp HOME, mockllm) but with the CEK ENABLED
# (cloud.e2ee defaults to true): a node WebCrypto client (j9-client.mjs) pairs,
# unwraps the CEK, sends an encrypted chat.send, and decrypts the replay —
# while Postgres and any CEK-less observer see ciphertext only.
#
# Asserts (docs/17 §6.1-§6.3, §6.6):
#   J9-S1  pairing request: node generates a P-256 key pair; POST
#          /api/v1/devices/{id}/pairings {label,kty:"P-256",pubkey} -> 201
#          {pairing_id, status:"pending"}
#   J9-S2  device approval: `jcode cloud pairings` lists the request;
#          `jcode cloud approve <pid>` exits 0 (wraps the CEK for the
#          requester pubkey and uploads it)
#   J9-S3  client unwrap: GET .../pairings/{pid} -> approved + wrap; node
#          reverses the ECIES wrap (ECDH + HKDF info "jcode-device-cek") and
#          recovers the CEK at key_gen=1
#   J9-S4  encrypted chat.send: client seals {text,channel:"console"} into an
#          envelope; POST sessions/new/messages {envelope} -> 202; the session
#          runs to idle (the device decrypted the command and relayed it)
#   J9-S5  server-side zero plaintext: psql over device_events/device_sessions
#          for the session shows only envelopes ({"enc":"aes-256-gcm",...}) —
#          neither the mockllm fragment "HELLO_FROM_JCODE" nor the user
#          message marker appears anywhere at rest
#   J9-S6  client decrypt replay: GET events returns envelopes; node decrypts
#          user_message (marker text, data.source="console") and agent_message
#          (carries the mockllm fragment) — round-trip integrity
#   J9-S7  unpaired view: a CEK-less client (plain curl, same user session)
#          GETs the same events and sees ciphertext envelopes, no plaintext
#   J9-S8  negatives: approving a nonexistent pairing fails; a pairing aged
#          past the 10-minute window reads expired and approve fails (409);
#          `jcode cloud deny` resolves a request to status=denied
#   J9-S9  M13 pairing gate (docs/17 §6.7): the device view echoes register
#          e2ee=true; plaintext bodies on messages/stop/approval get 409
#          pairing_required; a sealed envelope still passes (202)
#
# Sourced by e2e.sh (BASE/TOKEN exported) OR runnable standalone:
#   BASE=http://127.0.0.1:18080 ./j9-device-e2ee.sh
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

: "${JCODE_BIN:=$HERE/../../jcode/jcode}"
: "${J9_MOCK_PORT:=18082}"   # scratch port-forward to svc/mockllm:8081
: "${J9_WEB_PORT:=18087}"    # local `jcode web` bind (loopback only)
J9_NODE_CLIENT="$HERE/j9-client.mjs"

J9_USER_ID="e2edevice0000000000000000000000k1"
J9_SESSION_ID="e2edevice0000000000000000000000k2"
J9_SESSION_TOKEN="e2e-device-e2ee-session-token"
# Unique plaintext markers that must NEVER appear at rest (J9-S5/S7) and must
# survive the encrypt->relay->decrypt round trip (J9-S6).
J9_MARKER="J9-E2EE-MARKER-9f4b2c"
J9_MOCK_FRAGMENT="HELLO_FROM_JCODE"

# Run-scoped state (also used by j9_cleanup from e2e.sh's teardown trap).
J9_HOME=""; J9_WS=""; J9_WEB_PID=""; J9_MOCK_PF_PID=""; J9_DEVICE_ID=""

j9_psql() {
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -tAc "$1"
}

j9_seed_session() {
  local hash
  hash="$(printf '%s' "$J9_SESSION_TOKEN" | shasum -a 256 | awk '{print $1}')"
  j9_psql "INSERT INTO users (id, display_name, avatar_url, is_cluster_admin, created_at)
             VALUES ('$J9_USER_ID', 'e2e-e2ee-tester', '', false, now())
             ON CONFLICT (id) DO NOTHING;
           INSERT INTO sessions (id, user_id, token_hash, created_at, expires_at, revoked_at)
             VALUES ('$J9_SESSION_ID', '$J9_USER_ID', '$hash', now(), now() + interval '1 day', NULL)
             ON CONFLICT (id) DO UPDATE
               SET token_hash=EXCLUDED.token_hash, expires_at=EXCLUDED.expires_at, revoked_at=NULL;" \
    >/dev/null
}

# j9_cleanup stops the local processes (jcode web, mockllm port-forward) and
# drops the throwaway user; ON DELETE CASCADE removes its sessions, devices,
# device_tokens, device_sessions, device_events, device_commands and
# device_pairings. Safe to call when partially set up.
j9_cleanup() {
  [ -n "$J9_WEB_PID" ] && kill "$J9_WEB_PID" 2>/dev/null
  [ -n "$J9_MOCK_PF_PID" ] && kill "$J9_MOCK_PF_PID" 2>/dev/null
  [ -n "$J9_HOME" ] && rm -rf "$J9_HOME"
  [ -n "$J9_WS" ] && rm -rf "$J9_WS"
  J9_WEB_PID=""; J9_MOCK_PF_PID=""; J9_HOME=""; J9_WS=""
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -c "DELETE FROM users WHERE id='$J9_USER_ID'" >/dev/null 2>&1 || true
}

# User-session-authenticated helpers (the client API docs/17 §4.3/§6.3 requires
# a real user session, not the CONSOLE_TOKEN service principal).
j9_get_code() {
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $J9_SESSION_TOKEN" "$BASE$1"
}
j9_post_code() {
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $J9_SESSION_TOKEN" \
    -H 'Content-Type: application/json' -d "$2" "$BASE$1"
}

# j9_new_pairing -> creates a pairing request with the J9 client pubkey;
# prints the pairing_id. $1: label.
j9_new_pairing() {
  local pub resp
  pub="$(cat "$J9_HOME/client-pub.b64")"
  resp="$(j9_post_code "/api/v1/devices/$J9_DEVICE_ID/pairings" \
    "{\"label\":\"$1\",\"kty\":\"P-256\",\"pubkey\":\"$pub\"}")"
  [ "$(http_code "$resp")" = "201" ] || return 1
  printf '%s' "$(http_body "$resp")" | jq -r '.pairing_id // empty'
}

j9_run() {
  section "J9 · device relay E2E encryption (docs/17 §6 — M5, real jcode web + mockllm + WebCrypto client)"

  if [ ! -x "$JCODE_BIN" ]; then
    skip J9-S1 "jcode binary not found at $JCODE_BIN (build: make -C ../jcode build-binary)"
    return 0
  fi
  if ! command -v node >/dev/null 2>&1; then
    skip J9-S1 "node not found (J9 needs node:crypto webcrypto for the client side)"
    return 0
  fi

  # --- setup: seed, port-forward mockllm, temp HOME, real login -------------
  j9_cleanup
  if ! j9_seed_session; then
    fail J9-S1 "could not seed the e2e-e2ee-tester user/session into Postgres"
    return 1
  fi
  local me_code
  me_code="$(http_code "$(j9_get_code "/api/v1/me")")"
  if [ "$me_code" != "200" ]; then
    fail J9-S1 "seeded session does not resolve (GET /api/v1/me -> $me_code)"
    return 1
  fi

  kubectl --context "$KCTX" -n "$NAMESPACE" port-forward svc/mockllm \
    "$J9_MOCK_PORT:8081" >/tmp/j9-mockllm-pf.log 2>&1 &
  J9_MOCK_PF_PID=$!
  local mock_ready="false" i
  for i in $(seq 1 20); do
    if curl -sS -o /dev/null "http://127.0.0.1:$J9_MOCK_PORT/health" 2>/dev/null; then
      mock_ready="true"; break
    fi
    sleep 1
  done
  if [ "$mock_ready" != "true" ]; then
    fail J9-S1 "mockllm port-forward never became healthy (log: $(tail -2 /tmp/j9-mockllm-pf.log))"
    return 1
  fi
  info "  mockllm reachable at 127.0.0.1:$J9_MOCK_PORT"

  J9_HOME="$(mktemp -t j9-home.XXXXXX)"; rm -f "$J9_HOME"; mkdir -p "$J9_HOME/.jcode"
  J9_WS="$(mktemp -t j9-ws.XXXXXX)"; rm -f "$J9_WS"; mkdir -p "$J9_WS"
  # No cloud.e2ee key: the default (true) is exactly what J9 exercises — the
  # connector lazily generates the CEK at startup and seals everything uplink.
  cat >"$J9_HOME/.jcode/config.json" <<JSON
{
  "providers": {
    "mock": {
      "api_key": "dummy-key",
      "base_url": "http://127.0.0.1:$J9_MOCK_PORT/v1",
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

  # Real device-code login (same scrape-approve pattern as J7-S8 / J8).
  local log="$J9_HOME/login.log" login_pid cli_user="" login_rc=""
  HOME="$J9_HOME" "$JCODE_BIN" login --cloud "$BASE" --name e2e-j9 >"$log" 2>&1 &
  login_pid=$!
  for i in $(seq 1 30); do
    cli_user="$(grep -oE '[A-Z2-9]{4}-[A-Z2-9]{4}' "$log" 2>/dev/null | head -1)"
    [ -n "$cli_user" ] && break
    kill -0 "$login_pid" 2>/dev/null || break
    sleep 1
  done
  if [ -z "$cli_user" ]; then
    fail J9-S1 "jcode login did not print a user_code (log: $(tail -3 "$log"))"
    kill "$login_pid" 2>/dev/null; wait "$login_pid" 2>/dev/null
    return 1
  fi
  local auth_body auth_req
  auth_req="{\"user_code\":\"$cli_user\",\"approve\":true}"
  auth_body="$(http_body "$(curl -sS -w $'\n%{http_code}' -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $J9_SESSION_TOKEN" \
    -d "$auth_req" "$BASE/auth/device/authorize")")"
  if ! printf '%s' "$auth_body" | grep -q approved; then
    info "  DEBUG approve failed: cli_user=[$cli_user] req=[$auth_req] login.log:"
    tail -5 "$log" | sed 's/^/    /'
    fail J9-S1 "could not approve the CLI user_code ($auth_body)"
    kill "$login_pid" 2>/dev/null; wait "$login_pid" 2>/dev/null
    return 1
  fi
  for i in $(seq 1 30); do
    if ! kill -0 "$login_pid" 2>/dev/null; then wait "$login_pid"; login_rc=$?; break; fi
    sleep 1
  done
  if [ "$login_rc" != "0" ]; then
    fail J9-S1 "jcode login did not exit 0 (rc=$login_rc)"
    return 1
  fi
  J9_DEVICE_ID="$(jq -r '.device_id // empty' "$J9_HOME/.jcode/cloud.json" 2>/dev/null)"
  if [ -z "$J9_DEVICE_ID" ]; then
    fail J9-S1 "cloud.json missing device_id after login"
    return 1
  fi
  info "  device logged in: $J9_DEVICE_ID (real device-code flow)"

  ( cd "$J9_WS" && exec env HOME="$J9_HOME" "$JCODE_BIN" web \
      --port "$J9_WEB_PORT" --host 127.0.0.1 --open=false \
      >>"$J9_HOME/web.log" 2>&1 ) &
  J9_WEB_PID=$!
  local web_ready="false"
  for i in $(seq 1 30); do
    if [ "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$J9_WEB_PORT/api/health" 2>/dev/null)" = "200" ]; then
      web_ready="true"; break
    fi
    kill -0 "$J9_WEB_PID" 2>/dev/null || break
    sleep 1
  done
  if [ "$web_ready" != "true" ]; then
    fail J9-S1 "jcode web never became healthy (log: $(tail -5 "$J9_HOME/web.log"))"
    return 1
  fi
  info "  jcode web up on 127.0.0.1:$J9_WEB_PORT (pid $J9_WEB_PID)"

  local online=""
  for i in $(seq 1 60); do
    online="$(http_body "$(j9_get_code "/api/v1/devices/$J9_DEVICE_ID")" \
      | jq -r 'if .online == null then "" else .online end' 2>/dev/null)"
    [ "$online" = "true" ] && break
    sleep 1
  done
  if [ "$online" != "true" ]; then
    fail J9-S1 "device never came online; cannot continue J9 (log: $(tail -5 "$J9_HOME/web.log"))"
    return 1
  fi
  # The connector generated+persisted the CEK at startup (E2EE default on).
  local cek_present
  cek_present="$(jq -r 'has("cek") and (.cek|length > 0)' "$J9_HOME/.jcode/cloud.json" 2>/dev/null)"
  assert_eq J9-S1 "connector lazily generated the account CEK at startup" "true" "$cek_present"

  # --- J9-S1: pairing request ------------------------------------------------
  node "$J9_NODE_CLIENT" keygen "$J9_HOME/client-priv.b64" >"$J9_HOME/client-pub.b64" 2>"$J9_HOME/node.log" || {
    fail J9-S1 "node keygen failed ($(cat "$J9_HOME/node.log"))"
    return 1
  }
  local resp code pid
  resp="$(j9_post_code "/api/v1/devices/$J9_DEVICE_ID/pairings" \
    "{\"label\":\"e2e-client\",\"kty\":\"P-256\",\"pubkey\":\"$(cat "$J9_HOME/client-pub.b64")\"}")"
  code="$(http_code "$resp")"
  assert_eq J9-S1 "POST /api/v1/devices/{id}/pairings returns 201" "201" "$code"
  pid="$(printf '%s' "$(http_body "$resp")" | jq -r '.pairing_id // empty')"
  assert_nonempty J9-S1 "201 carries a pairing_id" "$pid"
  assert_eq J9-S1 "pairing starts pending" "pending" \
    "$(printf '%s' "$(http_body "$resp")" | jq -r '.status // empty')"
  if [ -z "$pid" ]; then
    fail J9-S2 "no pairing_id; cannot continue J9"
    return 1
  fi
  info "  pairing requested: $pid (label e2e-client)"

  # --- J9-S2: device approval via the real CLI ------------------------------
  local list_out
  list_out="$(HOME="$J9_HOME" "$JCODE_BIN" cloud pairings 2>&1)"
  assert_contains J9-S2 "jcode cloud pairings lists the pending request" "$list_out" "$pid"
  HOME="$J9_HOME" "$JCODE_BIN" cloud approve "$pid" >"$J9_HOME/approve.log" 2>&1
  assert_eq J9-S2 "jcode cloud approve exits 0 (CEK wrapped for the requester)" "0" "$?"

  # --- J9-S3: client polls the pairing and unwraps the CEK -------------------
  local pview pstatus
  pview="$(http_body "$(j9_get_code "/api/v1/devices/$J9_DEVICE_ID/pairings/$pid")")"
  pstatus="$(printf '%s' "$pview" | jq -r '.status // empty')"
  assert_eq J9-S3 "pairing resolves to approved" "approved" "$pstatus"
  printf '%s' "$pview" | jq -r '.wrap // empty' >"$J9_HOME/wrap.json"
  if [ ! -s "$J9_HOME/wrap.json" ]; then
    fail J9-S3 "approved pairing carries no wrap blob (view: $(printf '%.200s' "$pview"))"
    return 1
  fi
  local key_gen
  key_gen="$(node "$J9_NODE_CLIENT" unwrap "$J9_HOME/wrap.json" "$J9_HOME/client-priv.b64" "$J9_HOME/cek.json" 2>>"$J9_HOME/node.log")"
  assert_eq J9-S3 "node unwraps the CEK (ECDH+HKDF jcode-device-cek), key_gen=1" "1" "$key_gen"

  # --- J9-S4: encrypted chat.send --------------------------------------------
  printf '{"text":"%s create the file","channel":"console"}' "$J9_MARKER" >"$J9_HOME/plain.json"
  node "$J9_NODE_CLIENT" seal "$J9_HOME/cek.json" "$J9_HOME/plain.json" >"$J9_HOME/env.json" 2>>"$J9_HOME/node.log"
  resp="$(j9_post_code "/api/v1/devices/$J9_DEVICE_ID/sessions/new/messages" \
    "{\"envelope\":$(cat "$J9_HOME/env.json")}")"
  code="$(http_code "$resp")"
  assert_eq J9-S4 "POST sessions/new/messages {envelope} returns 202" "202" "$code"
  assert_nonempty J9-S4 "202 carries a command_id" \
    "$(printf '%s' "$(http_body "$resp")" | jq -r '.command_id // empty')"

  local sid="" sstatus="" sbody
  for i in $(seq 1 90); do
    sbody="$(http_body "$(j9_get_code "/api/v1/devices/$J9_DEVICE_ID/sessions")")"
    sid="$(printf '%s' "$sbody" | jq -r '.sessions[0].session_id // empty' 2>/dev/null)"
    if [ -n "$sid" ]; then
      sstatus="$(printf '%s' "$sbody" | jq -r --arg s "$sid" \
        '.sessions[] | select(.session_id==$s) | .status' 2>/dev/null)"
      [ "$sstatus" = "idle" ] && break
    fi
    sleep 1
  done
  assert_nonempty J9-S4 "session index gains a session after the encrypted chat.send" "$sid"
  assert_eq J9-S4 "session settles to idle (device decrypted + relayed the command)" "idle" "$sstatus"
  if [ -z "$sid" ]; then
    fail J9-S5 "no session mirrored; cannot continue J9 (log: $(tail -5 "$J9_HOME/web.log"))"
    return 1
  fi
  info "  cloud session $sid"

  # --- J9-S5: server-side zero plaintext (psql at rest) ----------------------
  local ev_rows ev_n meta_row
  ev_rows="$(j9_psql "SELECT convert_from(envelope,'UTF8') FROM device_events WHERE session_id='$sid' ORDER BY seq" 2>/dev/null)"
  ev_n="$(printf '%s\n' "$ev_rows" | grep -c .)"
  if [ "$ev_n" -gt 0 ]; then
    pass J9-S5 "device_events holds $ev_n rows for the session"
  else
    fail J9-S5 "device_events is empty for session $sid"
  fi
  local enc_n
  enc_n="$(printf '%s\n' "$ev_rows" | grep -c '"enc":"aes-256-gcm"')"
  assert_eq J9-S5 "every stored event envelope is aes-256-gcm ciphertext" "$ev_n" "$enc_n"
  assert_not_contains J9-S5 "no mockllm plaintext at rest (device_events)" "$ev_rows" "$J9_MOCK_FRAGMENT"
  assert_not_contains J9-S5 "no user message marker at rest (device_events)" "$ev_rows" "$J9_MARKER"
  meta_row="$(j9_psql "SELECT convert_from(meta,'UTF8') FROM device_sessions WHERE session_id='$sid'" 2>/dev/null)"
  assert_contains J9-S5 "device_sessions meta is a sealed envelope" "$meta_row" '"enc":"aes-256-gcm"'
  assert_not_contains J9-S5 "no plaintext in device_sessions meta" "$meta_row" "$J9_MARKER"

  # --- J9-S6: client decrypt replay ------------------------------------------
  local ev_body
  ev_body="$(http_body "$(j9_get_code "/api/v1/devices/$J9_DEVICE_ID/sessions/$sid/events?after_seq=0&limit=1000")")"
  local plain_all="" line kind p64
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    kind="${line%% *}"
    p64="${line#* }"
    printf '%s' "$p64" | base64 -d >"$J9_HOME/ev.json"
    plain_all="$plain_all
[$kind] $(node "$J9_NODE_CLIENT" open "$J9_HOME/cek.json" "$J9_HOME/ev.json" 2>>"$J9_HOME/node.log")"
  done <<EOF
$(printf '%s' "$ev_body" | jq -r '.events[] | select(.kind=="user_message" or .kind=="agent_message") | .kind + " " + (.payload | @base64)')
EOF
  assert_contains J9-S6 "decrypted user_message carries the marker text" "$plain_all" "$J9_MARKER"
  assert_contains J9-S6 "decrypted user_message marks data.source=console" "$plain_all" '"source":"console"'
  assert_contains J9-S6 "decrypted agent_message carries the mockllm fragment" "$plain_all" "$J9_MOCK_FRAGMENT"

  # --- J9-S7: unpaired (CEK-less) view of the same history --------------------
  assert_contains J9-S7 "raw events API returns ciphertext envelopes" "$ev_body" '"enc":"aes-256-gcm"'
  assert_not_contains J9-S7 "CEK-less client sees no mockllm plaintext" "$ev_body" "$J9_MOCK_FRAGMENT"
  assert_not_contains J9-S7 "CEK-less client sees no user message marker" "$ev_body" "$J9_MARKER"

  # --- J9-S9: M13 pairing gate (docs/17 §6.7) ----------------------------------
  # register 上报回显: this connector (CEK active, cloud.e2ee default on) must
  # have reported e2ee=true, echoed on the client device view.
  local e2ee_flag
  e2ee_flag="$(http_body "$(j9_get_code "/api/v1/devices/$J9_DEVICE_ID")" \
    | jq -r 'if .e2ee == null then "" else .e2ee end' 2>/dev/null)"
  assert_eq J9-S9 "device view echoes register e2ee=true (CEK active)" "true" "$e2ee_flag"

  # 明文注入被门拦: the seeded user session POSTing plaintext to the e2ee
  # device gets 409 pairing_required on all three command endpoints; nothing
  # is enqueued (the device could never decrypt it).
  resp="$(j9_post_code "/api/v1/devices/$J9_DEVICE_ID/sessions/$sid/messages" '{"text":"plaintext-injection"}')"
  assert_eq J9-S9 "plaintext {text} to an e2ee device returns 409" "409" "$(http_code "$resp")"
  assert_contains J9-S9 "409 error is pairing_required" "$(http_body "$resp")" "pairing_required"
  resp="$(j9_post_code "/api/v1/devices/$J9_DEVICE_ID/sessions/$sid/stop" '{}')"
  assert_eq J9-S9 "plaintext stop to an e2ee device returns 409" "409" "$(http_code "$resp")"
  resp="$(j9_post_code "/api/v1/devices/$J9_DEVICE_ID/sessions/$sid/approval" \
    '{"approval_id":"a1","decision":"approve"}')"
  assert_eq J9-S9 "plaintext approval to an e2ee device returns 409" "409" "$(http_code "$resp")"

  # 密文放行: a sealed envelope on the same session still goes through (202).
  printf '{"text":"%s gate-ping","channel":"console"}' "$J9_MARKER" >"$J9_HOME/plain-gate.json"
  node "$J9_NODE_CLIENT" seal "$J9_HOME/cek.json" "$J9_HOME/plain-gate.json" \
    >"$J9_HOME/env-gate.json" 2>>"$J9_HOME/node.log"
  resp="$(j9_post_code "/api/v1/devices/$J9_DEVICE_ID/sessions/$sid/messages" \
    "{\"envelope\":$(cat "$J9_HOME/env-gate.json")}")"
  assert_eq J9-S9 "sealed envelope to the e2ee device still returns 202" "202" "$(http_code "$resp")"

  # --- J9-S8: negative paths ---------------------------------------------------
  HOME="$J9_HOME" "$JCODE_BIN" cloud approve "00000000000000000000000000000000" \
    >"$J9_HOME/approve-404.log" 2>&1
  if [ "$?" != "0" ]; then
    pass J9-S8 "approving a nonexistent pairing fails (404)"
  else
    fail J9-S8 "approving a nonexistent pairing exited 0"
  fi

  local exp_pid exp_status
  exp_pid="$(j9_new_pairing "e2e-expired")"
  assert_nonempty J9-S8 "second pairing request created (to be expired)" "$exp_pid"
  if [ -n "$exp_pid" ]; then
    j9_psql "UPDATE device_pairings SET created_at = now() - interval '20 minutes' WHERE id='$exp_pid'" >/dev/null
    exp_status="$(http_body "$(j9_get_code "/api/v1/devices/$J9_DEVICE_ID/pairings/$exp_pid")" \
      | jq -r '.status // empty')"
    assert_eq J9-S8 "stale pending pairing reads expired (10-minute window)" "expired" "$exp_status"
    HOME="$J9_HOME" "$JCODE_BIN" cloud approve "$exp_pid" >"$J9_HOME/approve-exp.log" 2>&1
    if [ "$?" != "0" ]; then
      pass J9-S8 "approving an expired pairing fails (409 pairing_expired)"
    else
      fail J9-S8 "approving an expired pairing exited 0"
    fi
  fi

  local deny_pid deny_status
  deny_pid="$(j9_new_pairing "e2e-denied")"
  assert_nonempty J9-S8 "third pairing request created (to be denied)" "$deny_pid"
  if [ -n "$deny_pid" ]; then
    HOME="$J9_HOME" "$JCODE_BIN" cloud deny "$deny_pid" >"$J9_HOME/deny.log" 2>&1
    assert_eq J9-S8 "jcode cloud deny exits 0" "0" "$?"
    deny_status="$(http_body "$(j9_get_code "/api/v1/devices/$J9_DEVICE_ID/pairings/$deny_pid")" \
      | jq -r '.status // empty')"
    assert_eq J9-S8 "denied pairing reads status=denied" "denied" "$deny_status"
  fi
}

# Standalone execution.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  trap j9_cleanup EXIT
  j9_run
  rc=$?
  j9_cleanup
  print_summary 2>/dev/null || exit 1
  exit "$rc"
fi
