#!/usr/bin/env bash
# j3.sh — Journey J3 "parallel isolation": two concurrent runs on one project
# both succeed with disjoint event streams (no run_id crosstalk), both artifacts
# present, and (best-effort) overlapping in time.
#
# PRD cloud/docs/10-prd.md §5 J3 (S1..S6). Maps to AC-11.
#
# NOTE on diff content (documented in FINDINGS.md): the mock LLM produces a
# FIXED scripted change independent of the prompt, so run A and run B yield
# byte-identical diffs. J3-S6's "A's diff contains A not B" cannot be asserted
# against the mock. We instead assert the ISOLATION invariants that actually
# matter (disjoint per-run event spaces, independent Jobs, both artifacts), and
# record the diff-distinctness point as a known mock limitation, not a product
# failure.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

j3_run() {
  section "J3 · parallel isolation (two concurrent runs)"

  local pc pid pcode; pc="$(create_project "j3-demo" "$SEED_REPO")"
  pid="${pc%%$'\t'*}"; pcode="${pc##*$'\t'}"
  assert_eq J3-S1 "POST /projects returns 201" "201" "$pcode"
  assert_nonempty J3-S1 "created project has id" "$pid"
  [ -n "$pid" ] && register_project "$pid"
  [ -n "$pid" ] || { fail J3-S1 "cannot continue J3 without a project"; return 1; }

  # --- J3-S1 / J3-S2: fire two runs back-to-back --------------------------
  local rca rcb ridA ridB
  rca="$(create_run "$pid" "add a line A")"; ridA="${rca%%$'\t'*}"
  assert_nonempty J3-S1 "run A created" "$ridA"
  rcb="$(create_run "$pid" "add a line B")"; ridB="${rcb%%$'\t'*}"
  assert_nonempty J3-S2 "run B created" "$ridB"
  if [ -n "$ridA" ] && [ -n "$ridB" ] && [ "$ridA" != "$ridB" ]; then
    pass J3-S2 "run A and run B have distinct ids"
  else
    fail J3-S2 "run ids not distinct (A=$ridA B=$ridB)"
  fi
  # both appear in the project's run list
  local run_list; run_list="$(api_get "/projects/$pid/runs" | jq -r '.runs[].id')"
  assert_contains J3-S2 "run list contains A" "$run_list" "$ridA"
  assert_contains J3-S2 "run list contains B" "$run_list" "$ridB"
  [ -n "$ridA" ] && [ -n "$ridB" ] || { fail J3-S3 "cannot continue J3"; return 1; }
  info "J3 runs: A=$ridA B=$ridB"

  # --- J3-S3: observe both progressing; try to catch simultaneous running -
  # Poll both quickly for a window and record whether we ever see both active
  # (running/scheduling) at the same sample. If the node schedules them fast
  # enough that we miss the overlap, we degrade to "independent completion" and
  # flag it non-fatally (per task instructions).
  local overlap="false" i
  for i in $(seq 1 60); do
    local sa sb
    sa="$(run_status "$ridA")"; sb="$(run_status "$ridB")"
    case "$sa" in running) a_active=1;; scheduling) a_active=1;; *) a_active=0;; esac
    case "$sb" in running) b_active=1;; scheduling) b_active=1;; *) b_active=0;; esac
    if [ "$a_active" = 1 ] && [ "$b_active" = 1 ]; then overlap="true"; break; fi
    # stop early if both already terminal
    case "$sa$sb" in
      succeeded*succeeded|*failed*|*canceled*) : ;;
    esac
    case "$sa" in succeeded|failed|canceled) case "$sb" in succeeded|failed|canceled) break;; esac;; esac
    sleep 0.5
  done
  if [ "$overlap" = "true" ]; then
    pass J3-S3 "runs A and B were both active (running/scheduling) at the same time"
  else
    skip J3-S3 "did not observe simultaneous active window (fast scheduling); asserting independent completion instead (see FINDINGS.md)"
  fi

  # --- wait for both to converge ------------------------------------------
  local finA finB
  finA="$(wait_terminal "$ridA")"
  finB="$(wait_terminal "$ridB")"
  assert_eq J3-S3 "run A independently reached succeeded" "succeeded" "$finA"
  assert_eq J3-S3 "run B independently reached succeeded" "succeeded" "$finB"

  # --- J3-S4 / J3-S5: event streams are disjoint (no crosstalk) -----------
  # Every event returned for run A belongs to run A only. The API keys events by
  # run id in the path, so the strongest cross-talk check is: A's event set and
  # B's event set are independently well-formed (each unique/gapless from 1) and
  # the two runs' Jobs/ids never appear in each other's payloads.
  local evA evB
  evA="$(list_events "$ridA")"
  evB="$(list_events "$ridB")"
  assert_true J3-S4 "run A events are unique/gapless/monotonic from 1" "$(seq_monotonic "$evA")"
  assert_true J3-S5 "run B events are unique/gapless/monotonic from 1" "$(seq_monotonic "$evB")"
  # No B run-id string leaks into A's event payloads, and vice-versa.
  local aStr bStr
  aStr="$(printf '%s' "$evA" | jq -c '.')"
  bStr="$(printf '%s' "$evB" | jq -c '.')"
  assert_not_contains J3-S4 "run A event log does not mention run B's id" "$aStr" "$ridB"
  assert_not_contains J3-S5 "run B event log does not mention run A's id" "$bStr" "$ridA"
  # Each stream has its own terminal succeeded status event.
  assert_true J3-S4 "run A stream has terminal run.status(succeeded)" \
    "$(printf '%s' "$evA" | jq 'map(select(.type=="run.status" and .payload.status=="succeeded"))|length>0')"
  assert_true J3-S5 "run B stream has terminal run.status(succeeded)" \
    "$(printf '%s' "$evB" | jq 'map(select(.type=="run.status" and .payload.status=="succeeded"))|length>0')"

  # --- J3-S6: both artifacts present; independent worker Jobs -------------
  local artA artB
  artA="$(api_get "/runs/$ridA/artifact" | jq -r '.content // empty')"
  assert_nonempty J3-S6 "run A artifact present & non-empty" "$artA"
  artB="$(api_get "/runs/$ridB/artifact" | jq -r '.content // empty')"
  assert_nonempty J3-S6 "run B artifact present & non-empty" "$artB"
  # independent worker Jobs (distinct k8s_job_name per run)
  local jobA jobB
  jobA="$(get_run "$ridA" | jq -r '.k8s_job_name // empty')"
  jobB="$(get_run "$ridB" | jq -r '.k8s_job_name // empty')"
  assert_nonempty J3-S6 "run A has a k8s_job_name" "$jobA"
  assert_nonempty J3-S6 "run B has a k8s_job_name" "$jobB"
  if [ -n "$jobA" ] && [ -n "$jobB" ] && [ "$jobA" != "$jobB" ]; then
    pass J3-S6 "runs A and B have independent worker Jobs ($jobA != $jobB)"
  else
    fail J3-S6 "worker Jobs not independent (A=$jobA B=$jobB)"
  fi
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  j3_run
  print_summary 2>/dev/null || exit 1
fi
