#!/usr/bin/env bash
# j1.sh — Journey J1 "first use": zero → project → run → live events → diff.
#
# PRD cloud/docs/10-prd.md §5 J1 (S1..S8). This is the API/SSE equivalent of the
# console flow (the MVP e2e is headless; UI selectors like [new-project-btn] are
# the console agent's concern — here we assert the underlying API contract that
# every J1 step depends on, mapped to the same step IDs).
#
# Sourced by e2e.sh (BASE/TOKEN already exported) OR runnable standalone:
#   BASE=http://127.0.0.1:18080 TOKEN=dev-console-token ./j1.sh
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

j1_run() {
  section "J1 · first use (zero → diff)"

  # --- J1-S1: project list starts empty-able; API reachable ---------------
  # (Console renders the empty-state + [new-project-btn]; here we assert the
  #  backing list endpoint answers and the projects list is a well-formed array.)
  local list_resp list_code list_body
  list_resp="$(api_get_code "/projects")"
  list_code="$(http_code "$list_resp")"; list_body="$(http_body "$list_resp")"
  if [ "$list_code" = "200" ] && printf '%s' "$list_body" | jq -e '.projects | type=="array"' >/dev/null; then
    pass J1-S1 "GET /projects returns 200 with a projects array (list page backing)"
  else
    fail J1-S1 "GET /projects (code=$list_code)"
  fi

  # --- J1-S3: create project (name only), then attach the repo as a service
  # (S2 is the modal render — pure console; the machine-checkable contract is
  #  the two-step create the console submits, asserted here as S3.)
  local proj_resp proj_code proj_body pid
  proj_resp="$(api_post_code "/projects" "{\"name\":\"j1-demo\"}")"
  proj_code="$(http_code "$proj_resp")"; proj_body="$(http_body "$proj_resp")"
  assert_eq J1-S3 "POST /projects returns 201" "201" "$proj_code"
  pid="$(printf '%s' "$proj_body" | jq -r '.id // empty')"
  assert_nonempty J1-S3 "created project has id" "$pid"
  [ -n "$pid" ] && register_project "$pid"
  # list now contains demo
  local names; names="$(api_get "/projects" | jq -r '.projects[].name')"
  assert_contains J1-S3 "project list now contains j1-demo" "$names" "j1-demo"

  [ -n "$pid" ] || { fail J1-S4 "cannot continue J1 without a project id"; return 1; }

  local svc_resp svc_code svc_body sid
  svc_resp="$(api_post_code "/projects/$pid/services" \
    "{\"name\":\"default\",\"repo_url\":\"$SEED_REPO\",\"default_branch\":\"main\"}")"
  svc_code="$(http_code "$svc_resp")"; svc_body="$(http_body "$svc_resp")"
  assert_eq J1-S3 "POST /projects/{id}/services returns 201" "201" "$svc_code"
  sid="$(printf '%s' "$svc_body" | jq -r '.id // empty')"
  assert_nonempty J1-S3 "created service has id" "$sid"
  [ -n "$sid" ] || { fail J1-S4 "cannot continue J1 without a service id"; return 1; }

  # --- J1-S4: trigger a run (service-scoped), initial status queued --------
  local run_resp run_code run_body rid status0
  run_resp="$(api_post_code "/services/$sid/runs" \
    "{\"prompt\":$(jq -Rn --arg p 'append a Hello line to README' '$p')}")"
  run_code="$(http_code "$run_resp")"; run_body="$(http_body "$run_resp")"
  assert_eq J1-S4 "POST /services/{id}/runs returns 201" "201" "$run_code"
  rid="$(printf '%s' "$run_body" | jq -r '.id // empty')"
  status0="$(printf '%s' "$run_body" | jq -r '.status')"
  assert_nonempty J1-S4 "created run has run_id" "$rid"
  assert_eq J1-S4 "new run initial status is queued" "queued" "$status0"
  [ -n "$rid" ] || { fail J1-S5 "cannot continue J1 without a run id"; return 1; }
  info "J1 run id: $rid"

  # --- J1-S5: consume the SSE live stream WHILE the run executes -----------
  # curl -N with ?access_token= (the browser-EventSource-compatible auth path
  # from 11-api.md §2.3). Capture frames to a file; parse after the run ends.
  local sse_out; sse_out="$(mktemp -t jcloud-j1-sse.XXXXXX)"
  # max-time is a hard cap; the server closes the stream itself on terminal.
  curl -sS -N --max-time "$POLL_TIMEOUT" \
    "$API/runs/$rid/stream?after_seq=0&access_token=$TOKEN" >"$sse_out" 2>/dev/null &
  local sse_pid=$!

  # Assert the run reaches running within 30s (PRD J1-S5: ≤30s to running).
  local reached_running="false" i deadline
  deadline=$(( $(date +%s) + 30 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    case "$(run_status "$rid")" in
      running) reached_running="true"; break;;
      succeeded|failed|canceled) reached_running="true"; break;; # fast job: passed through running
    esac
    sleep 1
  done
  assert_true J1-S5 "run reaches running (or beyond) within 30s" "$reached_running"

  # --- J1-S6: run converges to succeeded, finished_at set -----------------
  local final; final="$(wait_terminal "$rid")"
  assert_eq J1-S6 "run terminal status is succeeded" "succeeded" "$final"
  local finished; finished="$(get_run "$rid" | jq -r '.finished_at // empty')"
  assert_nonempty J1-S6 "run.finished_at populated" "$finished"
  # timing: started_at also populated (PRD run model)
  local started; started="$(get_run "$rid" | jq -r '.started_at // empty')"
  assert_nonempty J1-S6 "run.started_at populated" "$started"

  # Wait for the SSE consumer to finish (server closes on terminal + max-time cap).
  wait "$sse_pid" 2>/dev/null || true

  # --- J1-S5/S6 stream assertions: parse captured SSE frames --------------
  # Extract the JSON data lines into an array and assert the taxonomy + seqs.
  local frames; frames="$(grep '^data: ' "$sse_out" | sed 's/^data: //' | jq -sc '.')"
  local n_frames; n_frames="$(printf '%s' "$frames" | jq 'length')"
  if [ "${n_frames:-0}" -gt 0 ]; then
    pass J1-S5 "SSE stream delivered $n_frames event frames (timeline > 0)"
  else
    fail J1-S5 "SSE stream delivered no event frames"
  fi
  have() { printf '%s' "$frames" | jq -e --arg t "$1" 'map(.type)|index($t)!=null' >/dev/null; }
  have agent.text        && pass J1-S5 "stream carried agent.text"        || fail J1-S5 "no agent.text in stream"
  have agent.tool_call   && pass J1-S5 "stream carried agent.tool_call"   || fail J1-S5 "no agent.tool_call in stream"
  have agent.tool_result && pass J1-S5 "stream carried agent.tool_result" || fail J1-S5 "no agent.tool_result in stream"
  have run.artifact      && pass J1-S6 "stream carried run.artifact"      || fail J1-S6 "no run.artifact in stream"
  # terminal run.status(succeeded) present in the stream
  if printf '%s' "$frames" | jq -e 'map(select(.type=="run.status" and .payload.status=="succeeded"))|length>0' >/dev/null; then
    pass J1-S6 "stream carried terminal run.status(succeeded)"
  else
    fail J1-S6 "no terminal run.status(succeeded) in stream"
  fi
  # unique + monotonic seqs in the stream (proves per-run seq authority / no collision)
  assert_true J1-S5 "stream seqs are unique & monotonic" \
    "$(printf '%s' "$frames" | jq '([.[].seq]) as $s | ($s == ($s|unique)) and ($s == ($s|sort))')"

  # --- J1-S7: artifact is a non-empty unified diff with the scripted change
  local art_resp art_code art art_content
  art_resp="$(api_get_code "/runs/$rid/artifact")"
  art_code="$(http_code "$art_resp")"; art="$(http_body "$art_resp")"
  assert_eq J1-S7 "GET /runs/{id}/artifact returns 200" "200" "$art_code"
  art_content="$(printf '%s' "$art" | jq -r '.content // empty')"
  assert_nonempty J1-S7 "artifact diff content is non-empty" "$art_content"
  assert_contains J1-S7 "artifact is a unified diff (has 'diff --git')" "$art_content" "diff --git"
  # The mock-scripted change writes HELLO_FROM_JCODE.txt (see runner/mockllm).
  assert_contains J1-S7 "artifact contains the mock-scripted change" "$art_content" "HELLO_FROM_JCODE.txt"
  # download variant returns 200 text/plain
  local dl_code; curl -sS -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer $TOKEN" "$API/runs/$rid/artifact?download=1" >/tmp/j1-dl-code 2>/dev/null
  dl_code="$(cat /tmp/j1-dl-code)"
  assert_eq J1-S7 "artifact ?download=1 returns 200" "200" "$dl_code"

  # --- J1-S8: refresh/replay consistency (persistence, not memory) --------
  # A fresh GET /events (cold read from Postgres) must equal the terminal shape:
  # same seq set as the stream, same terminal status. This is the "reload the
  # page" equivalent — no live subscription, pure durable replay.
  local ev_json; ev_json="$(list_events "$rid")"
  local ev_count stream_count
  ev_count="$(printf '%s' "$ev_json" | jq 'length')"
  stream_count="$(printf '%s' "$frames" | jq '[.[]|select(.type)]|length')"
  # The durable log should have >= the streamed count (stream may drop the final
  # comment-only terminal marker but every data frame is durable). Assert the
  # durable seq set is gapless/monotonic from 1 and the terminal status matches.
  assert_true J1-S8 "durable events (cold read) are unique/gapless/monotonic from 1" \
    "$(seq_monotonic "$ev_json")"
  local durable_final; durable_final="$(get_run "$rid" | jq -r '.status')"
  assert_eq J1-S8 "cold GET /runs/{id} still reports succeeded (persisted)" "succeeded" "$durable_final"
  # Replay via a SECOND cold SSE connection (after_seq=0) yields the same seq set.
  local replay; replay="$(curl -sS -N --max-time 15 \
    "$API/runs/$rid/stream?after_seq=0&access_token=$TOKEN" 2>/dev/null \
    | grep '^data: ' | sed 's/^data: //' | jq -sc '[.[].seq]')"
  local durable_seqs; durable_seqs="$(printf '%s' "$ev_json" | jq -c '[.[].seq]')"
  assert_eq J1-S8 "cold SSE replay seq set == durable event seq set" "$durable_seqs" "$replay"

  info "J1 event count (durable): $ev_count ; stream data frames: $stream_count"
  rm -f "$sse_out" /tmp/j1-dl-code 2>/dev/null || true

  # Export the run id so e2e.sh can use it for the latency spot-check.
  J1_RUN_ID="$rid"
}

# Standalone execution.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  j1_run
  print_summary 2>/dev/null || exit 1
fi
