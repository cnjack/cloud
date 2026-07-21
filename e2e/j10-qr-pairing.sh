#!/usr/bin/env bash
# j10-qr-pairing.sh — Journey J10 "scan-to-pair + desktop visual login/approval"
# (design contract: cloud/docs/17-jcode-device-relay.md §6.3, module M11).
# Drives the full M11 UX loop against the live orchestrator with a REAL
# `jcode web` process (temp HOME, mockllm as the model) — and, unlike J8/J9,
# NEVER touches the CLI: login, pairing-offer minting and pairing approval all
# go through the local web control plane (/api/cloud/*), exactly like the
# desktop app would.
#
# Asserts (docs/17 §6.3 — M11):
#   J10-S1  visual login: POST /api/cloud/login {cloud_url} -> 200 user_code;
#           the seeded user approves the device code; GET
#           /api/cloud/login/status reaches success; cloud.json is written;
#           the connector comes online (GET /api/v1/devices online=true) —
#           proving desktop login needs no CLI
#   J10-S2  scan-to-pair: POST /api/cloud/pairing-offer -> 200 with a
#           jcode://pair?cloud=..&device=..&offer=..&secret=.. QR string; a
#           node WebCrypto client (j9-client.mjs pattern) parses the QR,
#           generates P-256 and claims (POST /api/v1/pairing-offers/{id}/claim)
#           -> 201 {pairing_id, device_id}; NO approve action — the connector
#           auto-approves (offer_id in pairing.request); the pairing reads
#           approved with a wrap; the client unwraps the CEK, sends an
#           encrypted chat.send and decrypts the agent_message replay
#   J10-S3  negatives: wrong secret -> 403 (offer still claimable after);
#           second claim of a used offer -> 409 offer_claimed; an offer aged
#           past its window (psql time shift) -> 410 offer_expired
#   J10-S4  visual approval: a bare pairing (no offer) is parked in the
#           connector inbox; GET /api/cloud/pairings lists it pending; POST
#           /api/cloud/pairings/{id}/approve resolves it; the cloud pairing
#           reads approved with a valid wrap (desktop approval replaces
#           `jcode cloud approve`)
#
# Sourced by e2e.sh (BASE/TOKEN exported) OR runnable standalone:
#   BASE=http://127.0.0.1:18080 ./j10-qr-pairing.sh
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

: "${JCODE_BIN:=$HERE/../../jcode/jcode}"
: "${J10_MOCK_PORT:=18083}"   # scratch port-forward to svc/mockllm:8081
: "${J10_WEB_PORT:=18088}"    # local `jcode web` bind (loopback only)
J10_NODE_CLIENT="$HERE/j9-client.mjs"

J10_USER_ID="e2edevice0000000000000000000000m1"
J10_SESSION_ID="e2edevice0000000000000000000000m2"
J10_SESSION_TOKEN="e2e-qr-pairing-session-token"
J10_MARKER="J10-QR-MARKER-7d3a1e"
J10_MOCK_FRAGMENT="HELLO_FROM_JCODE"

# Run-scoped state (also used by j10_cleanup from e2e.sh's teardown trap).
J10_HOME=""; J10_WS=""; J10_WEB_PID=""; J10_MOCK_PF_PID=""; J10_DEVICE_ID=""

j10_psql() {
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -tAc "$1"
}

j10_seed_session() {
  local hash
  hash="$(printf '%s' "$J10_SESSION_TOKEN" | shasum -a 256 | awk '{print $1}')"
  j10_psql "INSERT INTO users (id, display_name, avatar_url, is_cluster_admin, created_at)
             VALUES ('$J10_USER_ID', 'e2e-qr-tester', '', false, now())
             ON CONFLICT (id) DO NOTHING;
           INSERT INTO sessions (id, user_id, token_hash, created_at, expires_at, revoked_at)
             VALUES ('$J10_SESSION_ID', '$J10_USER_ID', '$hash', now(), now() + interval '1 day', NULL)
             ON CONFLICT (id) DO UPDATE
               SET token_hash=EXCLUDED.token_hash, expires_at=EXCLUDED.expires_at, revoked_at=NULL;" \
    >/dev/null
}

# j10_cleanup stops the local processes (jcode web, mockllm port-forward) and
# drops the throwaway user; ON DELETE CASCADE removes its sessions, devices,
# device_tokens, device_sessions, device_events, device_commands,
# device_pairings and device_pairing_offers. Safe when partially set up.
j10_cleanup() {
  [ -n "$J10_WEB_PID" ] && kill "$J10_WEB_PID" 2>/dev/null
  [ -n "$J10_MOCK_PF_PID" ] && kill "$J10_MOCK_PF_PID" 2>/dev/null
  [ -n "$J10_HOME" ] && rm -rf "$J10_HOME"
  [ -n "$J10_WS" ] && rm -rf "$J10_WS"
  J10_WEB_PID=""; J10_MOCK_PF_PID=""; J10_HOME=""; J10_WS=""
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -c "DELETE FROM users WHERE id='$J10_USER_ID'" >/dev/null 2>&1 || true
}

# User-session-authenticated helpers (the client API docs/17 §4.3/§6.3 requires
# a real user session, not the CONSOLE_TOKEN service principal).
j10_get_code() {
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $J10_SESSION_TOKEN" "$BASE$1"
}
j10_post_code() {
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $J10_SESSION_TOKEN" \
    -H 'Content-Type: application/json' -d "$2" "$BASE$1"
}
# Local web control plane helpers (loopback, no auth — same as j8's /api/sessions).
j10_local_get() { curl -sS "http://127.0.0.1:$J10_WEB_PORT$1"; }
j10_local_post_code() {
  curl -sS -w $'\n%{http_code}' -H 'Content-Type: application/json' \
    -d "$2" "http://127.0.0.1:$J10_WEB_PORT$1"
}

# j10_mint_offer -> POST /api/cloud/pairing-offer on the local web backend;
# prints "<offer_id> <secret> <qr>".
j10_mint_offer() {
  local resp qr offer_id secret query
  resp="$(j10_local_post_code "/api/cloud/pairing-offer" '{}')"
  [ "$(http_code "$resp")" = "200" ] || return 1
  qr="$(printf '%s' "$(http_body "$resp")" | jq -r '.qr // empty')"
  offer_id="$(printf '%s' "$(http_body "$resp")" | jq -r '.offer_id // empty')"
  query="${qr#jcode://pair?}"
  secret="$(printf '%s' "$query" | tr '&' '\n' | sed -n 's/^secret=//p')"
  [ -n "$qr" ] && [ -n "$offer_id" ] && [ -n "$secret" ] || return 1
  printf '%s %s %s' "$offer_id" "$secret" "$qr"
}

j10_run() {
  section "J10 · scan-to-pair + desktop visual login/approval (docs/17 §6.3 — M11, real jcode web + mockllm, no CLI)"

  if [ ! -x "$JCODE_BIN" ]; then
    skip J10-S1 "jcode binary not found at $JCODE_BIN (build: make -C ../jcode build-binary)"
    return 0
  fi
  if ! command -v node >/dev/null 2>&1; then
    skip J10-S1 "node not found (J10 needs node:crypto webcrypto for the client side)"
    return 0
  fi

  # --- setup: seed, port-forward mockllm, temp HOME (NO CLI login) ----------
  j10_cleanup
  if ! j10_seed_session; then
    fail J10-S1 "could not seed the e2e-qr-tester user/session into Postgres"
    return 1
  fi
  local me_code
  me_code="$(http_code "$(j10_get_code "/api/v1/me")")"
  if [ "$me_code" != "200" ]; then
    fail J10-S1 "seeded session does not resolve (GET /api/v1/me -> $me_code)"
    return 1
  fi

  kubectl --context "$KCTX" -n "$NAMESPACE" port-forward svc/mockllm \
    "$J10_MOCK_PORT:8081" >/tmp/j10-mockllm-pf.log 2>&1 &
  J10_MOCK_PF_PID=$!
  local mock_ready="false" i
  for i in $(seq 1 20); do
    if curl -sS -o /dev/null "http://127.0.0.1:$J10_MOCK_PORT/health" 2>/dev/null; then
      mock_ready="true"; break
    fi
    sleep 1
  done
  if [ "$mock_ready" != "true" ]; then
    fail J10-S1 "mockllm port-forward never became healthy (log: $(tail -2 /tmp/j10-mockllm-pf.log))"
    return 1
  fi
  info "  mockllm reachable at 127.0.0.1:$J10_MOCK_PORT"

  J10_HOME="$(mktemp -t j10-home.XXXXXX)"; rm -f "$J10_HOME"; mkdir -p "$J10_HOME/.jcode"
  J10_WS="$(mktemp -t j10-ws.XXXXXX)"; rm -f "$J10_WS"; mkdir -p "$J10_WS"
  # No cloud.e2ee key: the default (true) is what J10-S2 exercises — the
  # connector generates the CEK at startup and the QR-paired client unwraps it.
  cat >"$J10_HOME/.jcode/config.json" <<JSON
{
  "providers": {
    "mock": {
      "api_key": "dummy-key",
      "base_url": "http://127.0.0.1:$J10_MOCK_PORT/v1",
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

  ( cd "$J10_WS" && exec env HOME="$J10_HOME" "$JCODE_BIN" web \
      --port "$J10_WEB_PORT" --host 127.0.0.1 --open=false \
      >>"$J10_HOME/web.log" 2>&1 ) &
  J10_WEB_PID=$!
  local web_ready="false"
  for i in $(seq 1 30); do
    if [ "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$J10_WEB_PORT/api/health" 2>/dev/null)" = "200" ]; then
      web_ready="true"; break
    fi
    kill -0 "$J10_WEB_PID" 2>/dev/null || break
    sleep 1
  done
  if [ "$web_ready" != "true" ]; then
    fail J10-S1 "jcode web never became healthy (log: $(tail -5 "$J10_HOME/web.log"))"
    return 1
  fi
  info "  jcode web up on 127.0.0.1:$J10_WEB_PORT (pid $J10_WEB_PID, fresh HOME — not logged in)"

  # --- J10-S1: visual login (device-code via the web backend, no CLI) --------
  local resp code body user_code
  resp="$(j10_local_post_code "/api/cloud/login" "{\"cloud_url\":\"$BASE\"}")"
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  assert_eq J10-S1 "POST /api/cloud/login returns 200" "200" "$code"
  user_code="$(printf '%s' "$body" | jq -r '.user_code // empty')"
  assert_nonempty J10-S1 "200 carries a user_code" "$user_code"
  assert_nonempty J10-S1 "200 carries a verification_uri" \
    "$(printf '%s' "$body" | jq -r '.verification_uri // empty')"
  if [ -z "$user_code" ]; then
    fail J10-S1 "no user_code; cannot continue J10 (body: $(printf '%.200s' "$body"))"
    return 1
  fi

  # Approve the device code with the seeded user session (same endpoint the
  # browser consent page posts to).
  local auth_body auth_resp
  auth_resp="$(curl -sS -w $'\n%{http_code}' -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $J10_SESSION_TOKEN" \
    -d "{\"user_code\":\"$user_code\",\"approve\":true}" "$BASE/auth/device/authorize")"
  auth_body="$(http_body "$auth_resp")"
  info "  authorize user_code=$user_code -> $(http_code "$auth_resp") $(printf '%.200s' "$auth_body")"
  assert_contains J10-S1 "seeded session approves the user_code" "$auth_body" "approved"

  local state=""
  for i in $(seq 1 90); do
    state="$(j10_local_get "/api/cloud/login/status" | jq -r '.state // empty' 2>/dev/null)"
    [ "$state" = "success" ] && break
    sleep 1
  done
  assert_eq J10-S1 "GET /api/cloud/login/status reaches success" "success" "$state"

  J10_DEVICE_ID="$(jq -r '.device_id // empty' "$J10_HOME/.jcode/cloud.json" 2>/dev/null)"
  assert_nonempty J10-S1 "cloud.json written with a device_id (login persisted, no CLI)" "$J10_DEVICE_ID"
  if [ -z "$J10_DEVICE_ID" ]; then
    fail J10-S1 "no device_id; cannot continue J10 (log: $(tail -5 "$J10_HOME/web.log"))"
    return 1
  fi

  local online="" dev_name
  for i in $(seq 1 60); do
    body="$(http_body "$(j10_get_code "/api/v1/devices")")"
    online="$(printf '%s' "$body" | jq -r --arg id "$J10_DEVICE_ID" \
      '.devices[]? | select(.id==$id) | .online' 2>/dev/null)"
    [ "$online" = "true" ] && break
    sleep 1
  done
  assert_eq J10-S1 "connector online after the web login (GET /api/v1/devices online=true)" "true" "$online"
  dev_name="$(printf '%s' "$body" | jq -r --arg id "$J10_DEVICE_ID" \
    '.devices[]? | select(.id==$id) | .name' 2>/dev/null)"
  assert_eq J10-S1 "device name defaults to the hostname (web login)" "$(hostname)" "$dev_name"
  if [ "$online" != "true" ]; then
    fail J10-S2 "device never came online; cannot continue J10 (log: $(tail -5 "$J10_HOME/web.log"))"
    return 1
  fi
  local cek_present=""
  for i in $(seq 1 30); do
    cek_present="$(jq -r 'has("cek") and (.cek|length > 0)' "$J10_HOME/.jcode/cloud.json" 2>/dev/null)"
    [ "$cek_present" = "true" ] && break
    sleep 1
  done
  assert_eq J10-S1 "connector generated the account CEK at startup (E2EE default on)" "true" "$cek_present"

  # --- J10-S2: scan-to-pair (offer claim -> auto-approve -> E2EE round trip) --
  local mint offer_id secret qr
  mint="$(j10_mint_offer)" || {
    fail J10-S2 "POST /api/cloud/pairing-offer did not return a usable offer"
    return 1
  }
  offer_id="$(printf '%s' "$mint" | awk '{print $1}')"
  secret="$(printf '%s' "$mint" | awk '{print $2}')"
  qr="$(printf '%s' "$mint" | awk '{print $3}')"
  info "  offer minted: $offer_id"
  assert_true J10-S2 "QR string matches jcode://pair?cloud=..&device=..&offer=..&secret=.." \
    "$(printf '%s' "$qr" | grep -qE '^jcode://pair\?cloud=[^&]+&device=[^&]+&offer=[^&]+&secret=[0-9a-f]{64}$' && echo true || echo false)"
  assert_contains J10-S2 "QR carries this offer_id" "$qr" "offer=$offer_id"
  assert_contains J10-S2 "QR carries this device_id" "$qr" "device=$J10_DEVICE_ID"

  node "$J10_NODE_CLIENT" keygen "$J10_HOME/client-priv.b64" >"$J10_HOME/client-pub.b64" 2>"$J10_HOME/node.log" || {
    fail J10-S2 "node keygen failed ($(cat "$J10_HOME/node.log"))"
    return 1
  }
  # The scanned client claims the offer: session auth + secret + P-256 pubkey.
  resp="$(j10_post_code "/api/v1/pairing-offers/$offer_id/claim" \
    "{\"secret\":\"$secret\",\"label\":\"e2e-qr-mobile\",\"kty\":\"P-256\",\"pubkey\":\"$(cat "$J10_HOME/client-pub.b64")\"}")"
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  assert_eq J10-S2 "POST /api/v1/pairing-offers/{id}/claim returns 201" "201" "$code"
  local pid
  pid="$(printf '%s' "$body" | jq -r '.pairing_id // empty')"
  assert_nonempty J10-S2 "201 carries a pairing_id" "$pid"
  assert_eq J10-S2 "201 carries the offering device_id" "$J10_DEVICE_ID" \
    "$(printf '%s' "$body" | jq -r '.device_id // empty')"
  if [ -z "$pid" ]; then
    fail J10-S2 "no pairing_id; cannot continue J10"
    return 1
  fi

  # NO approve action anywhere: the connector sees offer_id in the
  # pairing.request and auto-approves. Poll the pairing to approved.
  local pstatus="" pview
  for i in $(seq 1 90); do
    pview="$(http_body "$(j10_get_code "/api/v1/devices/$J10_DEVICE_ID/pairings/$pid")")"
    pstatus="$(printf '%s' "$pview" | jq -r '.status // empty')"
    [ "$pstatus" = "approved" ] && break
    sleep 1
  done
  assert_eq J10-S2 "pairing resolves to approved WITHOUT any approve action (QR auto-approve)" \
    "approved" "$pstatus"
  printf '%s' "$pview" | jq -r '.wrap // empty' >"$J10_HOME/wrap.json"
  if [ ! -s "$J10_HOME/wrap.json" ]; then
    fail J10-S2 "approved pairing carries no wrap blob (view: $(printf '%.200s' "$pview"))"
    return 1
  fi
  pass J10-S2 "approved pairing carries the CEK wrap"

  # The desktop notifier observes the auto-approval via last_paired.
  local lp=""
  for i in $(seq 1 30); do
    lp="$(j10_local_get "/api/cloud/pairings" | jq -r \
      --arg p "$pid" 'if .last_paired and .last_paired.pairing_id==$p then .last_paired.auto else "" end' 2>/dev/null)"
    [ -n "$lp" ] && break
    sleep 1
  done
  assert_eq J10-S2 "web inbox reports last_paired.auto=true for the QR pairing" "true" "$lp"

  local key_gen
  key_gen="$(node "$J10_NODE_CLIENT" unwrap "$J10_HOME/wrap.json" "$J10_HOME/client-priv.b64" "$J10_HOME/cek.json" 2>>"$J10_HOME/node.log")"
  assert_eq J10-S2 "client unwraps the CEK (ECDH+HKDF jcode-device-cek), key_gen=1" "1" "$key_gen"

  # Encrypted chat.send from the QR-paired client.
  printf '{"text":"%s create the file","channel":"console"}' "$J10_MARKER" >"$J10_HOME/plain.json"
  node "$J10_NODE_CLIENT" seal "$J10_HOME/cek.json" "$J10_HOME/plain.json" >"$J10_HOME/env.json" 2>>"$J10_HOME/node.log"
  resp="$(j10_post_code "/api/v1/devices/$J10_DEVICE_ID/sessions/new/messages" \
    "{\"envelope\":$(cat "$J10_HOME/env.json")}")"
  code="$(http_code "$resp")"
  assert_eq J10-S2 "encrypted chat.send (envelope) returns 202" "202" "$code"
  assert_nonempty J10-S2 "202 carries a command_id" \
    "$(printf '%s' "$(http_body "$resp")" | jq -r '.command_id // empty')"

  local sid="" sstatus="" sbody
  for i in $(seq 1 90); do
    sbody="$(http_body "$(j10_get_code "/api/v1/devices/$J10_DEVICE_ID/sessions")")"
    sid="$(printf '%s' "$sbody" | jq -r '.sessions[0].session_id // empty' 2>/dev/null)"
    if [ -n "$sid" ]; then
      sstatus="$(printf '%s' "$sbody" | jq -r --arg s "$sid" \
        '.sessions[] | select(.session_id==$s) | .status' 2>/dev/null)"
      [ "$sstatus" = "idle" ] && break
    fi
    sleep 1
  done
  assert_nonempty J10-S2 "session index gains a session after the encrypted chat.send" "$sid"
  assert_eq J10-S2 "session settles to idle (device decrypted + relayed the command)" "idle" "$sstatus"
  if [ -z "$sid" ]; then
    fail J10-S2 "no session mirrored; cannot finish J10-S2 (log: $(tail -5 "$J10_HOME/web.log"))"
    return 1
  fi

  # Decrypt the replay: user_message carries the marker, agent_message the
  # mockllm fragment — full scan-to-pair E2EE round trip.
  local ev_body plain_all="" line kind p64
  ev_body="$(http_body "$(j10_get_code "/api/v1/devices/$J10_DEVICE_ID/sessions/$sid/events?after_seq=0&limit=1000")")"
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    kind="${line%% *}"
    p64="${line#* }"
    printf '%s' "$p64" | base64 -d >"$J10_HOME/ev.json"
    plain_all="$plain_all
[$kind] $(node "$J10_NODE_CLIENT" open "$J10_HOME/cek.json" "$J10_HOME/ev.json" 2>>"$J10_HOME/node.log")"
  done <<EOF
$(printf '%s' "$ev_body" | jq -r '.events[] | select(.kind=="user_message" or .kind=="agent_message") | .kind + " " + (.payload | @base64)')
EOF
  assert_contains J10-S2 "decrypted user_message carries the marker text" "$plain_all" "$J10_MARKER"
  assert_contains J10-S2 "decrypted user_message marks data.source=console" "$plain_all" '"source":"console"'
  assert_contains J10-S2 "decrypted agent_message carries the mockllm fragment" "$plain_all" "$J10_MOCK_FRAGMENT"

  # --- J10-S3: negatives ------------------------------------------------------
  # Wrong secret -> 403, and the offer must remain claimable afterwards.
  local mint2 offer2 secret2
  mint2="$(j10_mint_offer)" || { fail J10-S3 "could not mint offer #2"; return 1; }
  offer2="${mint2%% *}"; secret2="$(printf '%s' "$mint2" | awk '{print $2}')"
  resp="$(j10_post_code "/api/v1/pairing-offers/$offer2/claim" \
    "{\"secret\":\"wrong-secret\",\"label\":\"e2e-bad\",\"kty\":\"P-256\",\"pubkey\":\"$(cat "$J10_HOME/client-pub.b64")\"}")"
  assert_eq J10-S3 "claim with a wrong secret returns 403" "403" "$(http_code "$resp")"
  resp="$(j10_post_code "/api/v1/pairing-offers/$offer2/claim" \
    "{\"secret\":\"$secret2\",\"label\":\"e2e-qr-mobile-2\",\"kty\":\"P-256\",\"pubkey\":\"$(cat "$J10_HOME/client-pub.b64")\"}")"
  code="$(http_code "$resp")"
  assert_eq J10-S3 "the same offer is still claimable after the 403" "201" "$code"
  # Second claim of the now-used offer -> 409 offer_claimed.
  resp="$(j10_post_code "/api/v1/pairing-offers/$offer2/claim" \
    "{\"secret\":\"$secret2\",\"label\":\"e2e-qr-mobile-3\",\"kty\":\"P-256\",\"pubkey\":\"$(cat "$J10_HOME/client-pub.b64")\"}")"
  code="$(http_code "$resp")"
  assert_eq J10-S3 "second claim of a used offer returns 409" "409" "$code"
  assert_contains J10-S3 "409 error is offer_claimed" "$(http_body "$resp")" "offer_claimed"

  # Expired offer -> 410 offer_expired (time shifted past the 10-minute window).
  local mint3 offer3 secret3
  mint3="$(j10_mint_offer)" || { fail J10-S3 "could not mint offer #3"; return 1; }
  offer3="${mint3%% *}"; secret3="$(printf '%s' "$mint3" | awk '{print $2}')"
  j10_psql "UPDATE device_pairing_offers SET expires_at = now() - interval '1 minute' WHERE id='$offer3'" >/dev/null
  resp="$(j10_post_code "/api/v1/pairing-offers/$offer3/claim" \
    "{\"secret\":\"$secret3\",\"label\":\"e2e-expired\",\"kty\":\"P-256\",\"pubkey\":\"$(cat "$J10_HOME/client-pub.b64")\"}")"
  code="$(http_code "$resp")"
  assert_eq J10-S3 "claim of an expired offer returns 410" "410" "$code"
  assert_contains J10-S3 "410 error is offer_expired" "$(http_body "$resp")" "offer_expired"

  # --- J10-S4: visual approval of a bare (non-offer) pairing ------------------
  node "$J10_NODE_CLIENT" keygen "$J10_HOME/client2-priv.b64" >"$J10_HOME/client2-pub.b64" 2>>"$J10_HOME/node.log"
  resp="$(j10_post_code "/api/v1/devices/$J10_DEVICE_ID/pairings" \
    "{\"label\":\"e2e-manual-client\",\"kty\":\"P-256\",\"pubkey\":\"$(cat "$J10_HOME/client2-pub.b64")\"}")"
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  assert_eq J10-S4 "bare POST /api/v1/devices/{id}/pairings returns 201" "201" "$code"
  local mpid
  mpid="$(printf '%s' "$body" | jq -r '.pairing_id // empty')"
  assert_eq J10-S4 "bare pairing starts pending" "pending" \
    "$(printf '%s' "$body" | jq -r '.status // empty')"
  if [ -z "$mpid" ]; then
    fail J10-S4 "no pairing_id for the bare pairing; cannot continue J10-S4"
    return 1
  fi

  # The connector parks the offer-less pairing.request in its inbox; the web
  # endpoint surfaces it (this replaces `jcode cloud pairings`).
  local parked=""
  for i in $(seq 1 90); do
    parked="$(j10_local_get "/api/cloud/pairings" | jq -r --arg p "$mpid" \
      '[.pairings[]? | select(.pairing_id==$p)] | length' 2>/dev/null)"
    [ "$parked" = "1" ] && break
    sleep 1
  done
  assert_eq J10-S4 "GET /api/cloud/pairings lists the bare pairing pending (inbox)" "1" "$parked"
  if [ "$parked" != "1" ]; then
    fail J10-S4 "pairing never reached the inbox (log: $(tail -5 "$J10_HOME/web.log"))"
    return 1
  fi

  resp="$(j10_local_post_code "/api/cloud/pairings/$mpid/approve" '{}')"
  assert_eq J10-S4 "POST /api/cloud/pairings/{id}/approve returns 200" "200" "$(http_code "$resp")"

  local mstatus="" mview
  for i in $(seq 1 60); do
    mview="$(http_body "$(j10_get_code "/api/v1/devices/$J10_DEVICE_ID/pairings/$mpid")")"
    mstatus="$(printf '%s' "$mview" | jq -r '.status // empty')"
    [ "$mstatus" = "approved" ] && break
    sleep 1
  done
  assert_eq J10-S4 "cloud pairing reads approved after the web approve" "approved" "$mstatus"
  printf '%s' "$mview" | jq -r '.wrap // empty' >"$J10_HOME/wrap2.json"
  local key_gen2=""
  if [ -s "$J10_HOME/wrap2.json" ]; then
    key_gen2="$(node "$J10_NODE_CLIENT" unwrap "$J10_HOME/wrap2.json" "$J10_HOME/client2-priv.b64" "$J10_HOME/cek2.json" 2>>"$J10_HOME/node.log")"
  fi
  assert_eq J10-S4 "web-approved wrap unwraps to the CEK (desktop approval replaces CLI approve)" \
    "1" "$key_gen2"
}

# Standalone execution.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  trap j10_cleanup EXIT
  j10_run
  rc=$?
  j10_cleanup
  print_summary 2>/dev/null || exit 1
  exit "$rc"
fi
