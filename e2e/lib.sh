#!/usr/bin/env bash
# lib.sh — shared helpers for the jcloud e2e suite.
#
# Sourced by e2e.sh and each journey script (j1.sh / j2.sh / j3.sh). Provides:
#   - a per-assertion PASS/FAIL recorder keyed by PRD step ID (Jx-Sn)
#   - curl wrappers that carry the console bearer token
#   - polling / SSE helpers
#   - cleanup registration (test projects are torn down at the end)
#
# Everything talks to the orchestrator over a caller-provided BASE url (a local
# port-forward set up by e2e.sh). Dependencies kept minimal: bash, curl, jq,
# kubectl (kubectl only used by e2e.sh for port-forward + secret + Job checks).

set -uo pipefail

# ---------------------------------------------------------------------------
# Config (overridable via env). e2e.sh exports BASE + TOKEN before sourcing the
# journeys; standalone runs of a journey script can export them too.
# ---------------------------------------------------------------------------
: "${KCTX:=orbstack}"
: "${NAMESPACE:=jcloud}"
: "${BASE:=http://127.0.0.1:18080}"
: "${TOKEN:=dev-console-token}"
: "${SEED_REPO:=git://git.jcloud.svc.cluster.local/seed.git}"
: "${BAD_REPO:=git://git.jcloud.svc.cluster.local/nonexistent.git}"
: "${POLL_TIMEOUT:=120}"     # seconds to wait for a run to reach a terminal state
: "${POLL_INTERVAL:=2}"      # seconds between status polls

API="$BASE/api/v1"

# ---------------------------------------------------------------------------
# Colours (disabled when not a tty or NO_COLOR set).
# ---------------------------------------------------------------------------
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_GREEN=$'\033[32m'; C_RED=$'\033[31m'; C_YELLOW=$'\033[33m'
  C_DIM=$'\033[2m'; C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
else
  C_GREEN=""; C_RED=""; C_YELLOW=""; C_DIM=""; C_BOLD=""; C_RESET=""
fi

# ---------------------------------------------------------------------------
# Assertion recorder. Results accumulate in a temp file (RESULTS_FILE) so they
# survive across the sub-scripts e2e.sh sources. Each line: STATUS<TAB>ID<TAB>DESC
# STATUS ∈ PASS | FAIL | SKIP.
# ---------------------------------------------------------------------------
: "${RESULTS_FILE:=$(mktemp -t jcloud-e2e-results.XXXXXX)}"
export RESULTS_FILE

_record() { # status id desc
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >>"$RESULTS_FILE"
}

# assert <ok?> <prd_id> <description>
# ok? is evaluated as: "true" (string) or exit-status via a preceding command.
# Usage patterns:
#   assert_true  "$cond" J1-S3 "POST /projects returns 201"
#   check <cmd...> && pass J1-S1 "..." || fail J1-S1 "..."
pass() { _record PASS "$1" "$2"; printf '%sPASS%s %-8s %s\n' "$C_GREEN" "$C_RESET" "$1" "$2"; }
fail() { _record FAIL "$1" "$2"; printf '%sFAIL%s %-8s %s\n' "$C_RED"   "$C_RESET" "$1" "$2"; }
skip() { _record SKIP "$1" "$2"; printf '%sSKIP%s %-8s %s\n' "$C_YELLOW" "$C_RESET" "$1" "$2"; }

# assert_eq <prd_id> <desc> <want> <got>
assert_eq() {
  if [ "$3" = "$4" ]; then pass "$1" "$2 (=$4)"
  else fail "$1" "$2 (want=$3 got=$4)"; fi
}
# assert_true <prd_id> <desc> <cond-string>  (cond-string == "true" passes)
assert_true() {
  if [ "$3" = "true" ]; then pass "$1" "$2"; else fail "$1" "$2 (got '$3')"; fi
}
# assert_nonempty <prd_id> <desc> <value>
assert_nonempty() {
  if [ -n "$3" ] && [ "$3" != "null" ]; then pass "$1" "$2"
  else fail "$1" "$2 (empty/null)"; fi
}
# assert_contains <prd_id> <desc> <haystack> <needle>
assert_contains() {
  if printf '%s' "$3" | grep -q -- "$4"; then pass "$1" "$2"
  else fail "$1" "$2 (missing '$4')"; fi
}
# assert_not_contains <prd_id> <desc> <haystack> <needle>
assert_not_contains() {
  if printf '%s' "$3" | grep -q -- "$4"; then fail "$1" "$2 (unexpectedly contains '$4')"
  else pass "$1" "$2"; fi
}

info() { printf '%s[e2e]%s %s\n' "$C_DIM" "$C_RESET" "$*"; }
section() { printf '\n%s== %s ==%s\n' "$C_BOLD" "$*" "$C_RESET"; }

# ---------------------------------------------------------------------------
# HTTP helpers (console bearer). All return the raw body on stdout. The *_code
# variants append the HTTP status on the last line.
# ---------------------------------------------------------------------------
api_get()  { curl -sS -H "Authorization: Bearer $TOKEN" "$API$1"; }
api_post() { curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$2" "$API$1"; }

# --- body+code helpers ------------------------------------------------------
# These emit the raw body followed by a final line "\n<http_code>". Callers use
# http_body() / http_code() to split locally. This avoids the classic bug of
# trying to set a global from inside $(...) (a subshell — the assignment would
# not propagate to the parent).
#
#   resp="$(api_post_code /projects "$json")"
#   code="$(http_code "$resp")"; body="$(http_body "$resp")"
api_post_code() {
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' -d "$2" "$API$1"
}
api_get_code() {
  curl -sS -w $'\n%{http_code}' -H "Authorization: Bearer $TOKEN" "$API$1"
}
# http_code <resp> -> the trailing status code line.
http_code() { printf '%s' "${1##*$'\n'}"; }
# http_body <resp> -> everything except the trailing status code line.
http_body() { printf '%s' "${1%$'\n'*}"; }

# ---------------------------------------------------------------------------
# Cleanup registry. Project ids appended here are DELETEd at suite teardown
# (cascades runs/events/artifacts; the reconciler's TTL reaps completed Jobs).
# ---------------------------------------------------------------------------
: "${CLEANUP_FILE:=$(mktemp -t jcloud-e2e-cleanup.XXXXXX)}"
export CLEANUP_FILE
register_project() { printf '%s\n' "$1" >>"$CLEANUP_FILE"; }

cleanup_projects() {
  [ -s "$CLEANUP_FILE" ] || return 0
  info "cleaning up test projects (cascades runs/events/artifacts)"
  while IFS= read -r pid; do
    [ -n "$pid" ] || continue
    curl -sS -o /dev/null -X DELETE -H "Authorization: Bearer $TOKEN" "$API/projects/$pid" || true
    info "  deleted project $pid"
  done <"$CLEANUP_FILE"
  # Best-effort: delete any leftover runner Jobs created by our runs (TTL would
  # reap them anyway, but this keeps the namespace tidy immediately).
  kubectl --context "$KCTX" -n "$NAMESPACE" delete jobs -l jcloud.run-id --ignore-not-found >/dev/null 2>&1 || true
}

# ---------------------------------------------------------------------------
# Domain helpers.
# ---------------------------------------------------------------------------

# create_project <name> <repo_url> -> prints "<id>\t<http_code>".
# (id may be empty on error; the code lets the caller assert 201.)
create_project() {
  local resp; resp="$(api_post_code "/projects" \
    "{\"name\":\"$1\",\"repo_url\":\"$2\",\"default_branch\":\"main\"}")"
  local code body id
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  id="$(printf '%s' "$body" | jq -r '.id // empty')"
  printf '%s\t%s' "$id" "$code"
}

# create_run <project_id> <prompt> -> prints "<id>\t<http_code>".
create_run() {
  local resp; resp="$(api_post_code "/projects/$1/runs" "{\"prompt\":$(jq -Rn --arg p "$2" '$p')}")"
  local code body id
  code="$(http_code "$resp")"; body="$(http_body "$resp")"
  id="$(printf '%s' "$body" | jq -r '.id // empty')"
  printf '%s\t%s' "$id" "$code"
}

# get_run <run_id> -> full run JSON.
get_run() { api_get "/runs/$1"; }

# run_status <run_id> -> status string.
run_status() { get_run "$1" | jq -r '.status // "?"'; }

# wait_terminal <run_id> -> prints final status; returns 0 if terminal reached.
wait_terminal() {
  local rid="$1" st="" i deadline
  deadline=$(( $(date +%s) + POLL_TIMEOUT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    st="$(run_status "$rid")"
    case "$st" in succeeded|failed|canceled) printf '%s' "$st"; return 0;; esac
    sleep "$POLL_INTERVAL"
  done
  printf '%s' "$st"; return 1
}

# list_events <run_id> -> events JSON array (the .events field).
list_events() { api_get "/runs/$1/events?limit=2000" | jq '.events'; }

# event_types <run_id> -> newline list of event types.
event_types() { list_events "$1" | jq -r '.[].type'; }

# seq_monotonic <events_json> -> "true" if seqs are unique, gapless, from 1.
seq_monotonic() {
  printf '%s' "$1" | jq '([.[].seq]) as $s | ($s|length) as $n | ($s == [range(1;$n+1)])'
}

# ---------------------------------------------------------------------------
# print_summary — render the per-assertion PASS/FAIL table grouped by journey,
# then a totals line. Returns non-zero if any FAIL was recorded (SKIP is not a
# failure). Safe to call from a standalone journey or from e2e.sh.
# ---------------------------------------------------------------------------
print_summary() {
  [ -s "$RESULTS_FILE" ] || { info "no assertions recorded"; return 0; }
  section "ASSERTION SUMMARY (PRD step traceability)"
  printf '%s%-6s %-10s %s%s\n' "$C_BOLD" "RESULT" "PRD-STEP" "ASSERTION" "$C_RESET"
  printf -- '------ ---------- --------------------------------------------------------\n'
  local status id desc colour
  while IFS=$'\t' read -r status id desc; do
    case "$status" in
      PASS) colour="$C_GREEN";; FAIL) colour="$C_RED";; SKIP) colour="$C_YELLOW";; *) colour="";;
    esac
    printf '%s%-6s%s %-10s %s\n' "$colour" "$status" "$C_RESET" "$id" "$desc"
  done <"$RESULTS_FILE"
  local n_pass n_fail n_skip
  n_pass="$(grep -c '^PASS' "$RESULTS_FILE" || true)"
  n_fail="$(grep -c '^FAIL' "$RESULTS_FILE" || true)"
  n_skip="$(grep -c '^SKIP' "$RESULTS_FILE" || true)"
  printf -- '------ ---------- --------------------------------------------------------\n'
  printf '%sPASS=%s%s  %sFAIL=%s%s  %sSKIP=%s%s\n' \
    "$C_GREEN" "$n_pass" "$C_RESET" "$C_RED" "$n_fail" "$C_RESET" "$C_YELLOW" "$n_skip" "$C_RESET"
  [ "${n_fail:-0}" -eq 0 ]
}

# ---------------------------------------------------------------------------
# latency_spotcheck <project_id> — rough e2e event latency (PRD §8, target p95
# ≤ 2s). Informational only; never gates the suite.
#
# Method: fire a fresh run and attach a LIVE SSE consumer from the moment of
# creation. For each frame as it arrives, record (local_receive_epoch − event.ts)
# = the delay between the server stamping the event and this client observing it.
# That is the transport+buffering component of the PRD's "emit → render" figure
# (it excludes only the browser paint, which a headless suite cannot measure).
# A negative delta (client clock slightly behind server) is clamped to 0.
# ---------------------------------------------------------------------------
latency_spotcheck() {
  local pid="$1"
  section "SSE latency spot-check (informational — PRD §8 p95 ≤ 2s)"
  if [ -z "$pid" ]; then info "no project id for latency run; skipping"; return 0; fi

  # Create a dedicated run and immediately open a live stream.
  local resp body rid
  resp="$(api_post_code "/projects/$pid/runs" "{\"prompt\":\"latency sample run\"}")"
  body="$(http_body "$resp")"
  rid="$(printf '%s' "$body" | jq -r '.id // empty')"
  if [ -z "$rid" ]; then info "could not create latency run; skipping"; return 0; fi
  info "latency run: $rid (live-tagging each frame's receive time)"

  local tmp; tmp="$(mktemp -t jcloud-lat.XXXXXX)"
  # Live consumer: tag every data frame with a high-res local epoch as it lands.
  ( curl -sS -N --max-time "$POLL_TIMEOUT" \
      "$API/runs/$rid/stream?after_seq=0&access_token=$TOKEN" 2>/dev/null \
    | while IFS= read -r line; do
        case "$line" in
          data:\ *) printf '%s\t%s\n' "$(perl -MTime::HiRes=time -e 'printf "%.3f", time')" "${line#data: }" ;;
        esac
      done >"$tmp" ) &
  local lat_pid=$!
  wait_terminal "$rid" >/dev/null 2>&1 || true
  # Give the streamer a moment to flush the terminal frames, then stop it.
  sleep 1; kill "$lat_pid" 2>/dev/null; wait "$lat_pid" 2>/dev/null || true

  if [ ! -s "$tmp" ]; then
    info "no frames captured for latency sample"
    rm -f "$tmp"; return 0
  fi
  # per-frame (receive_epoch − ts_epoch) in ms.
  local stats
  stats="$(awk -F'\t' '{
      recv=$1; json=$2;
      if (match(json, /"ts":"[^"]+"/)) { ts=substr(json,RSTART+6,RLENGTH-7); } else next;
      cmd="date -u -j -f %Y-%m-%dT%H:%M:%S \"" substr(ts,1,19) "\" +%s 2>/dev/null";
      cmd | getline epoch; close(cmd);
      frac=0; if (match(ts, /\.[0-9]+/)) { frac=substr(ts,RSTART,RLENGTH)+0; }
      tsec=epoch+frac;
      d=(recv - tsec)*1000; if (d<0) d=0;
      print d;
    }' "$tmp" | sort -n)"
  local count; count="$(printf '%s\n' "$stats" | grep -c . || true)"
  if [ "${count:-0}" -eq 0 ]; then
    info "latency: could not parse timestamps; skipping figures"
    rm -f "$tmp"; return 0
  fi
  local min max p50 p95 mean
  min="$(printf '%s\n' "$stats" | head -1)"
  max="$(printf '%s\n' "$stats" | tail -1)"
  p50="$(printf '%s\n' "$stats" | awk -v n="$count" 'NR==int((n+1)/2){print; exit}')"
  p95="$(printf '%s\n' "$stats" | awk -v n="$count" 'NR==int((n*0.95)+0.5){print; exit}')"
  mean="$(printf '%s\n' "$stats" | awk '{s+=$1} END{printf "%.0f", s/NR}')"
  info "latency samples=$count  min=${min}ms  p50=${p50}ms  p95=${p95}ms  max=${max}ms  mean=${mean}ms"
  local verdict="within"; awk "BEGIN{exit !($p95>2000)}" && verdict="ABOVE"
  info "PRD §8 target p95 ≤ 2000ms → measured p95=${p95}ms ($verdict target). Informational only; does not gate."
  info "(transport+buffer component of emit→render; excludes browser paint. Live-tagged, not replay.)"
  rm -f "$tmp"
}
