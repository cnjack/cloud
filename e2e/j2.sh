#!/usr/bin/env bash
# j2.sh — Journey J2 "failure visibility": bad repo → failed(clone_failed) →
# readable message → one-click retry (new run linked via retried_from).
#
# PRD cloud/docs/10-prd.md §5 J2 (S1..S5). Maps to AC-9 / AC-10.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

j2_run() {
  section "J2 · failure visibility (clone_failed + retry)"

  # --- J2-S1: project with an unreachable repo → run created, queued ------
  local pc pid pcode; pc="$(create_project "j2-bad" "$BAD_REPO")"
  pid="${pc%%$'\t'*}"; pcode="${pc##*$'\t'}"
  assert_eq J2-S1 "POST /projects (bad repo) returns 201" "201" "$pcode"
  assert_nonempty J2-S1 "created project has id" "$pid"
  [ -n "$pid" ] && register_project "$pid"
  [ -n "$pid" ] || { fail J2-S1 "cannot continue J2 without a project"; return 1; }

  local rc rid rcode; rc="$(create_run "$pid" "do something that needs the repo")"
  rid="${rc%%$'\t'*}"; rcode="${rc##*$'\t'}"
  assert_eq J2-S1 "POST /runs returns 201" "201" "$rcode"
  assert_nonempty J2-S1 "created run has id" "$rid"
  local st0; st0="$(printf '%s' "$(get_run "$rid")" | jq -r '.status')"
  # queued at creation (may already be scheduling by the time we read — accept both)
  case "$st0" in queued|scheduling) pass J2-S1 "run starts queued/scheduling (=$st0)";;
    *) fail J2-S1 "run initial status unexpected (=$st0)";; esac
  [ -n "$rid" ] || { fail J2-S2 "cannot continue J2 without a run"; return 1; }
  info "J2 run id: $rid"

  # --- J2-S2: event stream carries a clone-stage error/failure event ------
  local final; final="$(wait_terminal "$rid")"
  local ev_json; ev_json="$(list_events "$rid")"
  # The runner emits run.failure{reason:clone_failed,...} before exiting non-zero.
  if printf '%s' "$ev_json" | jq -e 'map(select(.type=="run.failure"))|length>0' >/dev/null; then
    pass J2-S2 "event stream contains a run.failure (clone-stage error) event"
  else
    fail J2-S2 "no run.failure event in the stream"
  fi

  # --- J2-S3: terminal failed + failure_reason + readable failure_message -
  assert_eq J2-S3 "terminal status is failed" "failed" "$final"
  local run_json reason msg
  run_json="$(get_run "$rid")"
  reason="$(printf '%s' "$run_json" | jq -r '.failure_reason // empty')"
  msg="$(printf '%s' "$run_json" | jq -r '.failure_message // empty')"
  # failure_reason must be in the enum; for an unreachable repo it should be clone_failed.
  case "$reason" in
    clone_failed|setup_failed|agent_error|timeout)
      pass J2-S3 "failure_reason in enum (=$reason)";;
    *) fail J2-S3 "failure_reason not in enum (=$reason)";;
  esac
  assert_eq J2-S3 "failure_reason is clone_failed (unreachable repo)" "clone_failed" "$reason"
  assert_nonempty J2-S3 "failure_message is non-empty (human-readable)" "$msg"

  # --- J2-S4: retry → 201, new run id != old, retried_from == old ---------
  local retry_resp retry_code retry_body new_rid retried_from
  retry_resp="$(api_post_code "/runs/$rid/retry" "")"
  retry_code="$(http_code "$retry_resp")"; retry_body="$(http_body "$retry_resp")"
  assert_eq J2-S4 "POST /runs/{id}/retry returns 201" "201" "$retry_code"
  new_rid="$(printf '%s' "$retry_body" | jq -r '.id // empty')"
  retried_from="$(printf '%s' "$retry_body" | jq -r '.retried_from // empty')"
  assert_nonempty J2-S4 "retry produced a new run id" "$new_rid"
  if [ -n "$new_rid" ] && [ "$new_rid" != "$rid" ]; then
    pass J2-S4 "retry run id differs from original"
  else
    fail J2-S4 "retry run id equals original ($new_rid)"
  fi
  assert_eq J2-S4 "retry.retried_from == original run id" "$rid" "$retried_from"
  local attempt; attempt="$(printf '%s' "$retry_body" | jq -r '.attempt')"
  assert_eq J2-S4 "retry.attempt == 2" "2" "$attempt"

  # The retried run (same bad repo) will also fail — let it converge so it does
  # not leak into teardown as a live Job. (Its project is registered already.)
  wait_terminal "$new_rid" >/dev/null 2>&1 || true

  # --- J2-S5: (optional) fix repo then retry → succeeded ------------------
  # PATCH the project's repo_url to the good seed repo, then retry the failed
  # original; the corrected run should reach succeeded (PRD J2-S5).
  local patch_code; api_get_code "/projects/$pid" >/dev/null # warm
  curl -sS -o /dev/null -w '%{http_code}' -X PATCH \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"repo_url\":\"$SEED_REPO\"}" "$API/projects/$pid" >/tmp/j2-patch-code 2>/dev/null
  patch_code="$(cat /tmp/j2-patch-code)"; rm -f /tmp/j2-patch-code
  if [ "$patch_code" = "200" ]; then
    local fix_resp fix_body fix_rid fix_final
    fix_resp="$(api_post_code "/runs/$rid/retry" "")"
    fix_body="$(http_body "$fix_resp")"
    fix_rid="$(printf '%s' "$fix_body" | jq -r '.id // empty')"
    if [ -n "$fix_rid" ]; then
      fix_final="$(wait_terminal "$fix_rid")"
      assert_eq J2-S5 "retry after fixing repo_url reaches succeeded" "succeeded" "$fix_final"
    else
      fail J2-S5 "could not create corrected retry run"
    fi
  else
    fail J2-S5 "PATCH project repo_url failed (code=$patch_code)"
  fi
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  j2_run
  print_summary 2>/dev/null || true
fi
