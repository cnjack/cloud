#!/usr/bin/env bash
# j5.sh — Journey J5 "multi-turn session smoke": a session:true run pauses in
# awaiting_input between turns, accepts follow-up messages, and converges to
# succeeded on finish — plus the fail-visible negative paths around it.
#
# Contract source of truth: cloud/docs/11-api.md §2.2 (POST .../runs {session},
# POST /runs/{id}/messages, POST /runs/{id}/finish) and §4 (event taxonomy:
# run.session, user.message, session.finish). Feature commits: F7 (D22, session
# core), F7a/F7b (runner loop + orchestrator awaiting_input), F9a (session
# transcript). Not the console — this is the headless API/event-contract
# equivalent, in the same spirit as j1-j4/j6.
#
# Covers (assertion ids J5-S1..S4; see J5-S5 note below):
#   J5-S1  create a session run (session:true) -> first turn completes ->
#          run reaches awaiting_input; events carry agent.text + run.session.
#   J5-S2  POST .../messages with a second prompt -> the run does a full
#          awaiting_input -> running -> awaiting_input round trip (proved via
#          the DURABLE event log, not live status polling, so it cannot be
#          missed by poll-interval granularity); events carry user.message;
#          the cumulative diff artifact carries BOTH rounds' personalised
#          output (mockllm's write_file scenario names the file after a hash
#          of the full prompt text, so round 2 writes a NEW file rather than
#          no-op'ing over round 1's — see runner/mockllm/main.go
#          scenarioForRequest / lastUserFingerprint).
#   J5-S3  POST .../finish -> run converges to succeeded; a session.finish
#          {reason:"user"} event is appended.
#   J5-S4  fail-visible negatives: POST .../messages on a NON-session run is a
#          typed 409 run_not_awaiting; POST .../messages with no bearer token,
#          or with a garbage one, is 401 unauthorized (docs/11-api.md §2.2 /
#          orchestrator/internal/api/api.go `authed`).
#
# F7b RACE — FOUND BY THIS JOURNEY, NOW FIXED (see the fix commit; regression
# asserted by J5-S1 below). History, for the record:
# turn 1 of a session run frequently finishes and calls POST .../turn-complete
# BEFORE the reconciler's own tick has observed the Job's pod as Running and
# called MarkRunning — with mockllm the whole first turn (clone + jcode/acpdrive
# startup + one LLM round trip + turn-hook diff upload) can complete in ~1.3s,
# well under the reconciler's 3s tick interval
# (orchestrator/internal/reconciler/reconciler.go NewReconciler / Tick;
# scheduling->running is gated on a LATER tick observing k8s.JobRunning, see
# decision.go:84-87). When that race was lost, handleTurnComplete's
# SetRunAwaitingInput(...) hit store.ErrInvalidTransition (running-only) and was
# SILENTLY dropped ("turn-complete on a non-running session run — ignoring") —
# the run then stayed stuck at whatever status it had (queued/scheduling) with
# the runner already long-polling next-prompt, invisibly, until session_ttl_secs
# (default 14400s) timed it out. A fail-SILENT outcome, exactly CLAUDE.md red
# line #1. Posting a message (J5-S2) happened to self-heal it, which is why the
# happy path limped along and the bug hid.
#
# THE FIX (orchestrator/internal/api/sessions.go, healRunToRunning): a
# turn-complete PROVES the pod is up and already ran a turn, so the handler now
# HEALS the run forward along the real, legal transition chain
# (queued->scheduling->running, using the existing store mutators — NO shortcut
# edge is added, so the D22 state history stays truthful) before parking it in
# awaiting_input. Each healed step is emitted, so the timeline shows the genuine
# running->awaiting_input transition. A concurrent reconciler MarkRunning is a
# harmless running->running no-op, and once the run reaches awaiting_input the
# reconciler leaves it alone (decision.go: awaiting_input + JobRunning -> none),
# so it is never knocked back. A turn-complete that genuinely races a terminal
# transition (cancel / dead pod) still returns a tolerated 200 but now logs a
# Warn (not a silent Info) — a stuck run would be visible. The same heal is
# applied on the first next-prompt poll (a first message that arrives while the
# run is still scheduling). J5-S1 asserts the FIXED behaviour: turn 1 reaches
# awaiting_input on its own, no message required.
#
# J5-S5 (permission_mode=approval / agent.permission_request) is NOT covered
# here. Checked first: runner/mockllm/main.go's scenario table (write_file,
# bash_write, review) has no scenario that drives jcode into an ACP
# RequestPermission call — mockllm only ever emits a scripted tool_call +
# final text, so there is no way to *trigger* a permission event through the
# mock LLM as it exists today. Server-side approval-response validation
# (400/404/409/agent.permission_resolved bookkeeping) IS unit-tested — see
# orchestrator/internal/api/permissions_test.go — but exercising the FULL
# loop end-to-end (a real permission_mode=approval run that actually emits
# agent.permission_request over this rig) would require either a real jcode +
# a tool policy that asks for permission, or extending mockllm with a new
# scenario; both are out of scope for this smoke journey, so J5-S5 is a
# recorded SKIP rather than a forced/fake assertion.
#
# Sourced by e2e.sh (BASE/TOKEN already exported) OR runnable standalone
# (assumes an existing port-forward + token, same convention as j1.sh):
#   BASE=http://127.0.0.1:18080 TOKEN=dev-console-token ./j5.sh
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

# --- local helpers (session-flow polling; not needed by the other journeys) -

# wait_awaiting_input <run_id> -> prints the run's status once it settles;
# returns 0 iff that status is awaiting_input. A terminal status reached
# without ever pausing (succeeded/failed/canceled) is treated as a genuine
# failure and returned immediately rather than spun on until POLL_TIMEOUT —
# a session run that skips awaiting_input entirely is a real bug, not a slow
# poll.
wait_awaiting_input() {
  local rid="$1" st="" deadline
  deadline=$(( $(date +%s) + POLL_TIMEOUT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    st="$(run_status "$rid")"
    case "$st" in
      awaiting_input) printf '%s' "$st"; return 0 ;;
      succeeded|failed|canceled) printf '%s' "$st"; return 1 ;;
    esac
    sleep "$POLL_INTERVAL"
  done
  printf '%s' "$st"; return 1
}

# max_seq <events_json> -> the highest .seq in the array (0 if empty).
max_seq() { printf '%s' "$1" | jq '[.[].seq] | (max // 0)'; }

# wait_round_trip <run_id> <after_seq> -> "true"/"false" on stdout (exit 0 on
# true). Polls the DURABLE event log (not live run status — a fast round can
# complete entirely between two status polls, so live polling for a transient
# "running" is racy) until it finds a run.status(running) event with
# seq > after_seq, FOLLOWED BY a run.status(awaiting_input) event with an even
# later seq. That pair is definitive proof of a full
# awaiting_input -> running -> awaiting_input cycle driven by the message
# posted after after_seq, regardless of how quickly the mock round completed.
wait_round_trip() {
  local rid="$1" after="$2" deadline
  deadline=$(( $(date +%s) + POLL_TIMEOUT ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    local evs running_seq awaiting_seq
    evs="$(list_events "$rid")"
    running_seq="$(printf '%s' "$evs" | jq --argjson s "$after" \
      '[.[] | select(.type=="run.status" and .payload.status=="running" and .seq>$s)] | (.[0].seq // empty)')"
    if [ -n "$running_seq" ] && [ "$running_seq" != "null" ]; then
      awaiting_seq="$(printf '%s' "$evs" | jq --argjson r "$running_seq" \
        '[.[] | select(.type=="run.status" and .payload.status=="awaiting_input" and .seq>$r)] | (.[0].seq // empty)')"
      if [ -n "$awaiting_seq" ] && [ "$awaiting_seq" != "null" ]; then
        printf 'true'; return 0
      fi
    fi
    case "$(run_status "$rid")" in failed|canceled) printf 'false'; return 1 ;; esac
    sleep "$POLL_INTERVAL"
  done
  printf 'false'; return 1
}

j5_run() {
  section "J5 · multi-turn session (create -> message -> finish)"

  # --- setup: one project + service shared by the whole journey ------------
  local pc pid sid pcode
  pc="$(create_project "j5-session" "$SEED_REPO")"
  pid="$(printf '%s' "$pc" | cut -f1)"
  sid="$(printf '%s' "$pc" | cut -f2)"
  pcode="$(printf '%s' "$pc" | cut -f3)"
  assert_eq J5-S1 "project + service create returns 201" "201" "$pcode"
  assert_nonempty J5-S1 "created project has id" "$pid"
  [ -n "$pid" ] && register_project "$pid"
  [ -n "$pid" ] && [ -n "$sid" ] || { fail J5-S1 "cannot continue J5 without a project/service"; return 1; }

  # ==========================================================================
  # J5-S1: create a session run -> round 1 completes -> awaiting_input
  # ==========================================================================
  local run_resp run_code run_body rid status0 session0
  run_resp="$(api_post_code "/services/$sid/runs" \
    "$(jq -Rn --arg p 'session round 1: write a short status note' '{prompt:$p, session:true}')")"
  run_code="$(http_code "$run_resp")"; run_body="$(http_body "$run_resp")"
  assert_eq J5-S1 "POST /services/{id}/runs {session:true} returns 201" "201" "$run_code"
  rid="$(printf '%s' "$run_body" | jq -r '.id // empty')"
  status0="$(printf '%s' "$run_body" | jq -r '.status')"
  session0="$(printf '%s' "$run_body" | jq -r '.session')"
  assert_nonempty J5-S1 "created run has id" "$rid"
  assert_eq J5-S1 "new session run initial status is queued" "queued" "$status0"
  assert_eq J5-S1 "created run echoes session=true" "true" "$session0"
  [ -n "$rid" ] || { fail J5-S1 "cannot continue J5 without a run id"; return 1; }
  info "J5 session run id: $rid"

  # REGRESSION (F7b heal): turn 1 must reach awaiting_input on its own, with NO
  # follow-up message — this is exactly the fast-turn race the header describes,
  # now healed in handleTurnComplete. A got=scheduling/running/queued here is the
  # bug reopening.
  local st1; st1="$(wait_awaiting_input "$rid")"
  assert_eq J5-S1 "run reaches awaiting_input after turn 1" "awaiting_input" "$st1"
  local persisted_session; persisted_session="$(get_run "$rid" | jq -r '.session')"
  assert_eq J5-S1 "run.session is persisted true" "true" "$persisted_session"

  local seq_after_turn1 evs1
  evs1="$(list_events "$rid")"
  seq_after_turn1="$(max_seq "$evs1")"
  have1() { printf '%s' "$evs1" | jq -e --arg t "$1" 'map(.type)|index($t)!=null' >/dev/null; }
  have1 agent.text    && pass J5-S1 "events carry agent.text (turn 1 output)"          || fail J5-S1 "no agent.text after turn 1"
  have1 run.session   && pass J5-S1 "events carry run.session (ACP session established)" || fail J5-S1 "no run.session event"
  if printf '%s' "$evs1" | jq -e 'map(select(.type=="run.status" and .payload.status=="awaiting_input"))|length>0' >/dev/null; then
    pass J5-S1 "events carry run.status(awaiting_input)"
  else
    fail J5-S1 "no run.status(awaiting_input) event"
  fi

  # ==========================================================================
  # J5-S2: POST a follow-up message -> round 2 (running -> awaiting_input) ---
  # ==========================================================================
  local msg_prompt="session round 2: write a second, different status note"
  local msg_resp msg_code msg_body msg_id msg_run_id msg_echo_prompt
  msg_resp="$(api_post_code "/runs/$rid/messages" "$(jq -Rn --arg p "$msg_prompt" '{prompt:$p}')")"
  msg_code="$(http_code "$msg_resp")"; msg_body="$(http_body "$msg_resp")"
  assert_eq J5-S2 "POST /runs/{id}/messages returns 201" "201" "$msg_code"
  msg_id="$(printf '%s' "$msg_body" | jq -r '.id // empty')"
  msg_run_id="$(printf '%s' "$msg_body" | jq -r '.run_id // empty')"
  msg_echo_prompt="$(printf '%s' "$msg_body" | jq -r '.prompt // empty')"
  assert_nonempty J5-S2 "queued message has an id" "$msg_id"
  assert_eq J5-S2 "queued message.run_id == the session run" "$rid" "$msg_run_id"
  assert_eq J5-S2 "queued message.prompt echoes the sent prompt" "$msg_prompt" "$msg_echo_prompt"

  local round_trip; round_trip="$(wait_round_trip "$rid" "$seq_after_turn1")"
  assert_true J5-S2 "run does a running -> awaiting_input round trip for turn 2 (durable log)" "$round_trip"
  local st2; st2="$(run_status "$rid")"
  assert_eq J5-S2 "run is back in awaiting_input after turn 2" "awaiting_input" "$st2"

  local evs2
  evs2="$(list_events "$rid")"
  if printf '%s' "$evs2" | jq -e --arg p "$msg_prompt" \
      'map(select(.type=="user.message" and .payload.prompt==$p))|length>0' >/dev/null; then
    pass J5-S2 "events carry the user.message bubble for round 2"
  else
    fail J5-S2 "no matching user.message event for round 2's prompt"
  fi
  # Both rounds produced agent output: at least two agent.text events overall.
  local text_count; text_count="$(printf '%s' "$evs2" | jq '[.[] | select(.type=="agent.text")] | length')"
  assert_true J5-S2 "at least two agent.text events across both rounds (got $text_count)" \
    "$([ "${text_count:-0}" -ge 2 ] && echo true || echo false)"

  # The cumulative diff artifact (upserted per turn) carries BOTH rounds' files
  # — mockllm personalises the write_file target by hashing the full prompt
  # text (JCODE_TASK_<fp>.txt), so round 2 writes a NEW file rather than
  # silently no-op'ing over round 1's (see runner/mockllm/main.go).
  local art_resp art_code art_body art_content distinct_files
  art_resp="$(api_get_code "/runs/$rid/artifact")"
  art_code="$(http_code "$art_resp")"; art_body="$(http_body "$art_resp")"
  assert_eq J5-S2 "GET /runs/{id}/artifact returns 200 mid-session" "200" "$art_code"
  art_content="$(printf '%s' "$art_body" | jq -r '.content // empty')"
  assert_nonempty J5-S2 "cumulative diff content is non-empty" "$art_content"
  distinct_files="$(printf '%s' "$art_content" | grep -oE 'JCODE_TASK_[0-9a-f]+\.txt' | sort -u | wc -l | tr -d ' ')"
  assert_true J5-S2 "cumulative diff contains two distinct per-round files (got $distinct_files)" \
    "$([ "${distinct_files:-0}" -ge 2 ] && echo true || echo false)"

  # ==========================================================================
  # J5-S3: finish -> run converges to succeeded; session.finish is recorded --
  # ==========================================================================
  local finish_resp finish_code
  finish_resp="$(api_post_code "/runs/$rid/finish" "")"
  finish_code="$(http_code "$finish_resp")"
  assert_eq J5-S3 "POST /runs/{id}/finish returns 200" "200" "$finish_code"

  local final; final="$(wait_terminal "$rid")"
  assert_eq J5-S3 "run converges to succeeded after finish" "succeeded" "$final"

  local evs3
  evs3="$(list_events "$rid")"
  if printf '%s' "$evs3" | jq -e \
      'map(select(.type=="session.finish" and .payload.reason=="user"))|length>0' >/dev/null; then
    pass J5-S3 "events carry session.finish {reason:user}"
  else
    fail J5-S3 "no session.finish{reason:user} event"
  fi

  # Idempotent repeat (docs/11-api.md: "重复 finish / 对已终态 run finish 均回 200").
  local refinish_code
  refinish_code="$(http_code "$(api_post_code "/runs/$rid/finish" "")")"
  assert_eq J5-S3 "repeat finish on a terminal run is idempotent (200)" "200" "$refinish_code"

  # ==========================================================================
  # J5-S4: fail-visible negatives around /messages
  # ==========================================================================
  # (a) a NON-session run: messages -> 409 run_not_awaiting. Cheap — the
  # session check happens before any status check, so a freshly-queued
  # single-shot run rejects immediately with no need to wait for it to run.
  local rc2 rid2
  rc2="$(create_run "$sid" "a plain single-shot run, not a session")"
  rid2="${rc2%%$'\t'*}"
  assert_nonempty J5-S4 "plain (non-session) run created for the negative test" "$rid2"
  if [ -n "$rid2" ]; then
    local neg_resp neg_code neg_body neg_err
    neg_resp="$(api_post_code "/runs/$rid2/messages" '{"prompt":"should be rejected"}')"
    neg_code="$(http_code "$neg_resp")"; neg_body="$(http_body "$neg_resp")"
    neg_err="$(printf '%s' "$neg_body" | jq -r '.error.code // empty')"
    assert_eq J5-S4 "messages on a non-session run returns 409" "409" "$neg_code"
    assert_eq J5-S4 "error.code == run_not_awaiting" "run_not_awaiting" "$neg_err"
  fi

  # (b) no bearer token -> 401 unauthorized (auth middleware runs before the
  # handler ever looks at the run, so any run id — including the terminal
  # session run above — demonstrates it).
  local noauth_code noauth_body noauth_err
  noauth_body="$(curl -sS -w $'\n%{http_code}' -H 'Content-Type: application/json' \
    -d '{"prompt":"x"}' "$API/runs/$rid/messages")"
  noauth_code="$(http_code "$noauth_body")"
  noauth_err="$(http_body "$noauth_body" | jq -r '.error.code // empty')"
  assert_eq J5-S4 "messages with no bearer token returns 401" "401" "$noauth_code"
  assert_eq J5-S4 "error.code == unauthorized (no token)" "unauthorized" "$noauth_err"

  # (c) garbage bearer token -> 401 unauthorized (same code path: an
  # unresolvable token is indistinguishable from a missing one).
  local badtok_body badtok_code badtok_err
  badtok_body="$(curl -sS -w $'\n%{http_code}' -H 'Authorization: Bearer not-a-real-token-12345' \
    -H 'Content-Type: application/json' -d '{"prompt":"x"}' "$API/runs/$rid/messages")"
  badtok_code="$(http_code "$badtok_body")"
  badtok_err="$(http_body "$badtok_body" | jq -r '.error.code // empty')"
  assert_eq J5-S4 "messages with an invalid bearer token returns 401" "401" "$badtok_code"
  assert_eq J5-S4 "error.code == unauthorized (bad token)" "unauthorized" "$badtok_err"

  # A "viewer role" 403 negative (as opposed to "no identity at all") is
  # deliberately NOT exercised here: authorizeProject() correctly returns 403
  # for a viewer (orchestrator/internal/api/sessions.go handleSendMessage
  # requires RoleMember; see TestSendMessagePermission in sessions_test.go for
  # the unit-level proof), but the CONSOLE_TOKEN this whole suite authenticates
  # with always resolves to the cluster-admin service principal
  # (orchestrator/internal/api/principal.go isClusterAdmin) — there is no HTTP
  # endpoint to mint a plain member/viewer user + session token; the only path
  # is the full Gitea OAuth round trip j6-webhook.sh already drives (browser-
  # facing Gitea + orchestrator port-forwards, consent form, etc). Wiring that
  # up here just to flip one status code from 401 to 403 is out of proportion
  # for a smoke journey, so it is left to the unit test rather than forced
  # into a fake e2e assertion.
  skip J5-S4 "viewer-role 403 on /messages not exercised (needs a real OAuth-minted non-admin user, see j6-webhook.sh oauth_login_gitea; covered at unit level by sessions_test.go TestSendMessagePermission)"

  # ==========================================================================
  # J5-S5: permission_mode=approval / agent.permission_request — SKIPPED
  # ==========================================================================
  skip J5-S5 "no mockllm scenario can trigger an ACP RequestPermission call (runner/mockllm/main.go scenarios are write_file/bash_write/review, none ask for permission) — see the header comment for the full rationale; server-side approval-response validation is unit-tested in orchestrator/internal/api/permissions_test.go instead"
}

# Standalone execution.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  # Fast-fail if the rig is unreachable, instead of hanging on curl's default
  # (long) connect timeout across every subsequent call. Mirrors e2e.sh's own
  # preflight healthz probe; this is a thin extra guard for standalone runs
  # (e2e.sh already establishes its own port-forward + healthz wait before
  # sourcing any journey).
  if ! curl -sS -o /dev/null --max-time 5 "$BASE/healthz" 2>/dev/null; then
    echo "j5.sh: orchestrator not reachable at $BASE/healthz within 5s." >&2
    echo "  Bring the rig up first, e.g.:" >&2
    echo "    kubectl config use-context orbstack" >&2
    echo "    make -C ../deploy build && make -C ../deploy up" >&2
    echo "    kubectl -n jcloud port-forward svc/orchestrator 18080:8080 &" >&2
    echo "  then: BASE=http://127.0.0.1:18080 TOKEN=<CONSOLE_TOKEN from secret/orchestrator-secret> ./j5.sh" >&2
    exit 5
  fi
  j5_run
  print_summary 2>/dev/null || exit 1
fi
