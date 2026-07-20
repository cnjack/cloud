#!/usr/bin/env bash
# j7-device-login.sh — Journey J7 "jcode device-code login" (design contract:
# cloud/docs/17-jcode-device-relay.md §3/§4.1, module M2). Exercises the RFC
# 8628 device-code flow against the live orchestrator plus the device uplink
# endpoints, and finishes with a REAL `jcode login/logout` CLI run against the
# port-forward (temp HOME, so the developer's ~/.jcode is untouched).
#
# Asserts (docs/17 §3.1/§3.2/§4.1):
#   J7-S1  POST /auth/device/code returns device_code/user_code(XXXX-XXXX)/
#          verification_uri/expires_in/interval
#   J7-S2  unapproved poll -> 400 authorization_pending
#   J7-S3  a real user session approves the user_code -> 200 approved
#   J7-S4  next poll -> 200 jcd_ token + device_id; a replay poll ->
#          400 token_already_redeemed
#   J7-S5  POST /internal/v1/device/register (device token) -> 200, same
#          device_id, heartbeat_interval=30
#   J7-S6  POST /internal/v1/device/heartbeat -> 204
#   J7-S7  negatives: bogus device token register -> 401; CONSOLE_TOKEN
#          (service principal) authorize -> 400; deny flow -> access_denied;
#          unknown device_code -> 400 expired_token
#   J7-S8  real CLI: jcode login --cloud $BASE (user_code scraped from stdout
#          and approved via the seeded session) -> exit 0, ~/.jcode/cloud.json
#          0600 with a jcd_ device_token; login --status shows the device;
#          logout exits 0 and removes cloud.json (remote revoke 404 is
#          tolerated by the CLI — the endpoint does not exist yet).
#
# A user session is REQUIRED for /auth/device/authorize (CONSOLE_TOKEN is a
# service principal and gets 400 by design). There is no public "create user"
# endpoint in this deployment, so the suite seeds a throwaway user + session
# straight into Postgres (display_name e2e-device-tester, fixed plaintext token
# e2e-device-session-token stored as its sha256 hex — the same HashToken the
# resolver applies). j7_cleanup deletes the user row, which cascades to the
# session and to every devices/device_tokens row minted under it; it runs both
# at j7 start (idempotent re-runs) and from e2e.sh's teardown trap.
#
# Sourced by e2e.sh (BASE/TOKEN exported) OR runnable standalone (needs a real
# CONSOLE_TOKEN for the J7-S7 service-principal check):
#   BASE=http://127.0.0.1:18080 TOKEN=<console-token> ./j7-device-login.sh
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

# The jcode CLI binary exercised by J7-S8 (build with `make build-binary` in the
# sibling jcode repo). Override with JCODE_BIN=/path/to/jcode.
: "${JCODE_BIN:=$HERE/../../jcode/jcode}"

J7_USER_ID="e2edevice00000000000000000000001"
J7_SESSION_ID="e2edevice00000000000000000000002"
J7_SESSION_TOKEN="e2e-device-session-token"

j7_psql() {
  kubectl --context "$KCTX" -n "$NAMESPACE" exec deploy/postgres -- \
    psql -U jcloud -d jcloud -c "$1"
}

# j7_seed_session (re)creates the throwaway user + a live session for the fixed
# test token. Idempotent: fixed ids, ON CONFLICT refresh un-revokes/re-expires.
j7_seed_session() {
  local hash
  hash="$(printf '%s' "$J7_SESSION_TOKEN" | shasum -a 256 | awk '{print $1}')"
  j7_psql "INSERT INTO users (id, display_name, avatar_url, is_cluster_admin, created_at)
             VALUES ('$J7_USER_ID', 'e2e-device-tester', '', false, now())
             ON CONFLICT (id) DO NOTHING;
           INSERT INTO sessions (id, user_id, token_hash, created_at, expires_at, revoked_at)
             VALUES ('$J7_SESSION_ID', '$J7_USER_ID', '$hash', now(), now() + interval '1 day', NULL)
             ON CONFLICT (id) DO UPDATE
               SET token_hash=EXCLUDED.token_hash, expires_at=EXCLUDED.expires_at, revoked_at=NULL;" \
    >/dev/null
}

# j7_cleanup drops the throwaway user; ON DELETE CASCADE removes its sessions
# and every device (+ device_tokens) approved by it during the run. Best effort.
j7_cleanup() {
  j7_psql "DELETE FROM users WHERE id='$J7_USER_ID'" >/dev/null 2>&1 || true
}

# j7_post_code <path> <bearer-token-or-empty> <json> — like api_post_code but
# for the non-/api/v1 device-auth endpoints and a caller-chosen credential.
j7_post_code() {
  local path="$1" tok="$2" json="$3"
  local args=(-sS -w $'\n%{http_code}' -H 'Content-Type: application/json' -d "$json")
  [ -n "$tok" ] && args+=(-H "Authorization: Bearer $tok")
  curl "${args[@]}" "$BASE$path"
}

# j7_authorize <user_code> <approve:true|false> [token] -> body+code of
# POST /auth/device/authorize with the seeded user session (default).
j7_authorize() {
  j7_post_code "/auth/device/authorize" "${3:-$J7_SESSION_TOKEN}" \
    "{\"user_code\":\"$1\",\"approve\":$2}"
}

j7_run() {
  section "J7 · jcode device-code login (docs/17 §3/§4.1 — M2)"

  # Idempotent re-runs: drop leftovers from a previous (possibly aborted) run,
  # then seed a fresh user session.
  j7_cleanup
  if ! j7_seed_session; then
    fail J7-S1 "could not seed the e2e-device-tester user/session into Postgres"
    return 1
  fi
  local me me_code me_body
  me="$(curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $J7_SESSION_TOKEN" "$BASE/api/v1/me")"
  me_code="$(http_code "$me")"; me_body="$(http_body "$me")"
  if [ "$me_code" = "200" ] && printf '%s' "$me_body" | grep -q 'e2e-device-tester'; then
    info "  seeded session resolves: GET /api/v1/me -> e2e-device-tester"
  else
    fail J7-S1 "seeded session does not resolve (GET /api/v1/me -> $me_code: $me_body)"
    return 1
  fi

  # --- J7-S1: start a flow --------------------------------------------------
  local resp code body device_code user_code
  resp="$(j7_post_code "/auth/device/code" "" '{"client_name":"e2e-j7"}')"
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  assert_eq J7-S1 "POST /auth/device/code returns 200" "200" "$code"
  device_code="$(printf '%s' "$body" | jq -r '.device_code // empty')"
  user_code="$(printf '%s' "$body" | jq -r '.user_code // empty')"
  if printf '%s' "$device_code" | grep -qE '^[0-9a-f]{64}$'; then
    pass J7-S1 "device_code is a 64-hex secret"
  else
    fail J7-S1 "device_code has unexpected shape ('$device_code')"
  fi
  if printf '%s' "$user_code" | grep -qE '^[A-Z2-9]{4}-[A-Z2-9]{4}$'; then
    pass J7-S1 "user_code is human format XXXX-XXXX ($user_code)"
  else
    fail J7-S1 "user_code has unexpected shape ('$user_code')"
  fi
  assert_contains J7-S1 "verification_uri points at the console /device route" \
    "$(printf '%s' "$body" | jq -r '.verification_uri // empty')" "/device"
  assert_eq J7-S1 "expires_in is 600 (RFC 8628 window)" "600" \
    "$(printf '%s' "$body" | jq -r '.expires_in // empty')"
  assert_eq J7-S1 "interval is 5 (poll cadence hint)" "5" \
    "$(printf '%s' "$body" | jq -r '.interval // empty')"
  [ -n "$device_code" ] || { fail J7-S2 "cannot continue J7 without a device_code"; return 1; }

  # --- J7-S2: pending poll ---------------------------------------------------
  resp="$(j7_post_code "/auth/device/token" "" "{\"device_code\":\"$device_code\"}")"
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  assert_eq J7-S2 "unapproved poll returns 400" "400" "$code"
  assert_contains J7-S2 "error is authorization_pending" "$body" "authorization_pending"

  # --- J7-S3: the user approves ----------------------------------------------
  resp="$(j7_authorize "$user_code" true)"
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  assert_eq J7-S3 "session approve returns 200" "200" "$code"
  assert_contains J7-S3 "flow status is approved" "$body" "approved"

  # --- J7-S4: redeem ----------------------------------------------------------
  resp="$(j7_post_code "/auth/device/token" "" "{\"device_code\":\"$device_code\"}")"
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  assert_eq J7-S4 "poll after approval returns 200" "200" "$code"
  local dev_token device_id
  dev_token="$(printf '%s' "$body" | jq -r '.access_token // empty')"
  device_id="$(printf '%s' "$body" | jq -r '.device_id // empty')"
  assert_eq J7-S4 "token_type is device" "device" \
    "$(printf '%s' "$body" | jq -r '.token_type // empty')"
  case "$dev_token" in
    jcd_*) pass J7-S4 "access_token carries the jcd_ device prefix";;
    *) fail J7-S4 "access_token missing jcd_ prefix ('${dev_token:0:8}…')";;
  esac
  assert_nonempty J7-S4 "device_id returned at issuance" "$device_id"
  # One-shot redemption: a replayed device_code must never mint a second token.
  resp="$(j7_post_code "/auth/device/token" "" "{\"device_code\":\"$device_code\"}")"
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  assert_eq J7-S4 "replayed device_code returns 400" "400" "$code"
  assert_contains J7-S4 "replay error is token_already_redeemed" "$body" "token_already_redeemed"
  [ -n "$dev_token" ] || { fail J7-S5 "cannot continue J7 without a device token"; return 1; }

  # --- J7-S5: device register --------------------------------------------------
  local pubkey="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
  resp="$(j7_post_code "/internal/v1/device/register" "$dev_token" \
    "{\"name\":\"e2e-j7\",\"hostname\":\"e2e-host\",\"jcode_version\":\"e2e\",\"pubkey\":\"$pubkey\"}")"
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  assert_eq J7-S5 "POST /internal/v1/device/register returns 200" "200" "$code"
  assert_eq J7-S5 "register echoes the issuance device_id" "$device_id" \
    "$(printf '%s' "$body" | jq -r '.device_id // empty')"
  assert_eq J7-S5 "heartbeat_interval is 30" "30" \
    "$(printf '%s' "$body" | jq -r '.heartbeat_interval // empty')"
  assert_nonempty J7-S5 "server_time returned" \
    "$(printf '%s' "$body" | jq -r '.server_time // empty')"

  # --- J7-S6: heartbeat ---------------------------------------------------------
  resp="$(j7_post_code "/internal/v1/device/heartbeat" "$dev_token" '{}')"
  code="$(http_code "$resp")"
  assert_eq J7-S6 "POST /internal/v1/device/heartbeat returns 204" "204" "$code"

  # --- J7-S7: negative paths -----------------------------------------------------
  # (a) a bogus jcd_ token is rejected by the principal resolver.
  resp="$(j7_post_code "/internal/v1/device/register" \
    "jcd_0000000000000000000000000000000000000000000000000000000000000000" \
    "{\"name\":\"x\",\"pubkey\":\"$pubkey\"}")"
  assert_eq J7-S7 "register with an unknown device token returns 401" "401" \
    "$(http_code "$resp")"
  # (b) the CONSOLE_TOKEN service principal can never approve (docs/17 §3.1).
  resp="$(j7_authorize "$user_code" true "$TOKEN")"
  assert_eq J7-S7 "authorize with CONSOLE_TOKEN returns 400 (user session required)" "400" \
    "$(http_code "$resp")"
  # (c) deny flow: fresh code -> deny -> poll answers access_denied.
  local d_code d_user
  resp="$(j7_post_code "/auth/device/code" "" '{"client_name":"e2e-j7-deny"}')"
  d_code="$(printf '%s' "$(http_body "$resp")" | jq -r '.device_code // empty')"
  d_user="$(printf '%s' "$(http_body "$resp")" | jq -r '.user_code // empty')"
  if [ -n "$d_code" ] && [ -n "$d_user" ]; then
    resp="$(j7_authorize "$d_user" false)"
    assert_contains J7-S7 "deny returns 200 with status denied" "$(http_body "$resp")" "denied"
    resp="$(j7_post_code "/auth/device/token" "" "{\"device_code\":\"$d_code\"}")"
    assert_eq J7-S7 "poll after deny returns 400" "400" "$(http_code "$resp")"
    assert_contains J7-S7 "deny error is access_denied" "$(http_body "$resp")" "access_denied"
  else
    fail J7-S7 "could not start the deny flow (device_code/user_code empty)"
  fi
  # (d) an unknown device_code is indistinguishable from an expired one.
  resp="$(j7_post_code "/auth/device/token" "" \
    '{"device_code":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}')"
  assert_eq J7-S7 "unknown device_code poll returns 400" "400" "$(http_code "$resp")"
  assert_contains J7-S7 "unknown device_code error is expired_token" "$(http_body "$resp")" "expired_token"

  # --- J7-S8: the real CLI --------------------------------------------------------
  if [ ! -x "$JCODE_BIN" ]; then
    skip J7-S8 "jcode binary not found at $JCODE_BIN (build: make -C ../jcode build-binary, or set JCODE_BIN)"
    return 0
  fi
  local home_dir log login_pid cli_user="" login_rc="" i
  home_dir="$(mktemp -t j7-home.XXXXXX)"; rm -f "$home_dir"; mkdir -p "$home_dir"
  log="$home_dir/login.log"
  HOME="$home_dir" "$JCODE_BIN" login --cloud "$BASE" --name e2e-device >"$log" 2>&1 &
  login_pid=$!
  for i in $(seq 1 30); do
    cli_user="$(grep -oE '[A-Z2-9]{4}-[A-Z2-9]{4}' "$log" 2>/dev/null | head -1)"
    [ -n "$cli_user" ] && break
    kill -0 "$login_pid" 2>/dev/null || break
    sleep 1
  done
  if [ -z "$cli_user" ]; then
    fail J7-S8 "jcode login did not print a user_code (log: $(tail -3 "$log" 2>/dev/null))"
    kill "$login_pid" 2>/dev/null; wait "$login_pid" 2>/dev/null
    rm -rf "$home_dir"
    return 1
  fi
  info "  CLI login started (pid $login_pid), user_code $cli_user — approving via seeded session"
  resp="$(j7_authorize "$cli_user" true)"
  assert_contains J7-S8 "CLI user_code approved via session" "$(http_body "$resp")" "approved"
  for i in $(seq 1 30); do
    if ! kill -0 "$login_pid" 2>/dev/null; then
      wait "$login_pid"; login_rc=$?
      break
    fi
    sleep 1
  done
  if [ -z "$login_rc" ]; then
    kill "$login_pid" 2>/dev/null; wait "$login_pid" 2>/dev/null
    fail J7-S8 "jcode login still running 30s after approval (killed)"
  else
    assert_eq J7-S8 "jcode login exits 0 after approval" "0" "$login_rc"
  fi
  local cjson="$home_dir/.jcode/cloud.json"
  if [ -f "$cjson" ]; then
    pass J7-S8 "~/.jcode/cloud.json exists after login"
    assert_eq J7-S8 "cloud.json permissions are 0600" "600" "$(stat -f '%Lp' "$cjson")"
    case "$(jq -r '.device_token // empty' "$cjson")" in
      jcd_*) pass J7-S8 "cloud.json holds a jcd_ device_token";;
      *) fail J7-S8 "cloud.json device_token missing jcd_ prefix";;
    esac
    assert_nonempty J7-S8 "cloud.json device_id set" "$(jq -r '.device_id // empty' "$cjson")"
    assert_eq J7-S8 "cloud.json device_name is e2e-device" "e2e-device" \
      "$(jq -r '.device_name // empty' "$cjson")"
  else
    fail J7-S8 "~/.jcode/cloud.json missing after login"
  fi
  local st_out
  st_out="$(HOME="$home_dir" "$JCODE_BIN" login --status 2>&1)"
  assert_contains J7-S8 "login --status reports the cloud url" "$st_out" "cloud url:"
  assert_contains J7-S8 "login --status reports the device id" "$st_out" "device id:"
  HOME="$home_dir" "$JCODE_BIN" logout >"$home_dir/logout.log" 2>&1
  assert_eq J7-S8 "jcode logout exits 0 (remote revoke 404 tolerated)" "0" "$?"
  if [ ! -e "$cjson" ]; then
    pass J7-S8 "logout removes ~/.jcode/cloud.json"
  else
    fail J7-S8 "cloud.json still present after logout"
  fi
  rm -rf "$home_dir"
}

# Standalone execution.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  j7_run
  rc=$?
  j7_cleanup
  print_summary 2>/dev/null || exit 1
  exit "$rc"
fi
