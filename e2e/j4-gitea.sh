#!/usr/bin/env bash
# j4-gitea.sh — Journey J4 "Gitea draft-PR closed loop" (PRD traceability id
# J-MR / AC-ST, stretch goal ST-1): a run on a git_mode=draft_pr project ends in
# a reviewable DRAFT pull request on the in-cluster Gitea, not just a diff.
#
# PRD cloud/docs/10-prd.md §3.3 ST-1 + J1-(ST). Asserts:
#   - the project can be created with git_mode=draft_pr + gitea provider config
#   - the run reaches succeeded
#   - the event stream carries a run.git event with an agent/run-* branch
#   - run.pr_url is non-empty (orchestrator opened the draft PR)
#   - the Gitea API shows that PR: it is a draft (WIP), head=agent/run-<id>,
#     base=main, and NOT merged (hard gate: never auto-merge)
#   - the diff artifact is STILL present (draft-PR is additive to IN-8)
#
# Requires the Gitea stack from deploy/base/gitea (make up runs the bootstrap
# Job). If Gitea / its token is not present, the whole journey SKIPs (it is a
# stretch goal, so a stack without Gitea must not fail the suite).
#
# Sourced by e2e.sh (BASE/TOKEN exported) OR runnable standalone:
#   BASE=http://127.0.0.1:18080 TOKEN=dev-console-token ./j4-gitea.sh
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

# In-cluster Gitea clone/push URL for the seed repo the bootstrap Job created.
: "${GITEA_INCLUSTER_URL:=http://gitea.jcloud.svc.cluster.local:3000}"
: "${GITEA_ORG:=jcloud}"
: "${GITEA_REPO:=seed}"
# The clone URL the runner (in-cluster) uses for REPO_URL. http (no auth needed
# for a public repo clone; the PUSH uses the token the orchestrator injects).
: "${GITEA_SEED_REPO:=${GITEA_INCLUSTER_URL}/${GITEA_ORG}/${GITEA_REPO}.git}"

# gitea_api METHOD PATH — call the Gitea API from a throwaway in-cluster pod
# (the orchestrator port-forward only exposes the orchestrator, not Gitea). Uses
# the token from the gitea-orchestrator secret. Prints the response body.
gitea_api() {
  local method="$1" path="$2"
  kubectl --context "$KCTX" -n "$NAMESPACE" run "gitea-api-$RANDOM" \
    --rm -i --restart=Never --image=curlimages/curl:latest --quiet --command -- \
    curl -sS -X "$method" -H "Authorization: token $GITEA_TOKEN" \
    "${GITEA_INCLUSTER_URL}/api/v1${path}" 2>/dev/null
}

j4_run() {
  section "J4 · Gitea draft-PR closed loop (ST-1 / J-MR)"

  # --- Preconditions: Gitea deployed + a token available -------------------
  if ! kubectl --context "$KCTX" -n "$NAMESPACE" get deploy/gitea >/dev/null 2>&1; then
    skip J-MR "Gitea not deployed (deploy/base/gitea absent); ST-1 is a stretch goal"
    return 0
  fi
  GITEA_TOKEN="$(kubectl --context "$KCTX" -n "$NAMESPACE" get secret gitea-orchestrator \
    -o jsonpath='{.data.GITEA_TOKEN}' 2>/dev/null | base64 -d 2>/dev/null)"
  if [ -z "$GITEA_TOKEN" ]; then
    skip J-MR "gitea-orchestrator secret/token absent (bootstrap Job not complete); skipping ST-1"
    return 0
  fi
  info "Gitea token loaded from secret/gitea-orchestrator (${#GITEA_TOKEN} chars)"

  # --- J4-S1: create a draft_pr project pointing at the Gitea seed repo -----
  local proj_resp proj_code proj_body pid
  proj_resp="$(api_post_code "/projects" \
    "{\"name\":\"j4-draftpr\",\"repo_url\":\"$GITEA_SEED_REPO\",\"default_branch\":\"main\",\"git_mode\":\"draft_pr\",\"provider\":\"gitea\",\"provider_url\":\"$GITEA_INCLUSTER_URL\",\"provider_repo\":\"$GITEA_ORG/$GITEA_REPO\"}")"
  proj_code="$(http_code "$proj_resp")"; proj_body="$(http_body "$proj_resp")"
  assert_eq J-MR "POST /projects (draft_pr) returns 201" "201" "$proj_code"
  pid="$(printf '%s' "$proj_body" | jq -r '.id // empty')"
  assert_nonempty J-MR "created draft_pr project has id" "$pid"
  local gm; gm="$(printf '%s' "$proj_body" | jq -r '.git_mode // empty')"
  assert_eq J-MR "project git_mode is draft_pr" "draft_pr" "$gm"
  [ -n "$pid" ] && register_project "$pid"
  [ -n "$pid" ] || { fail J-MR "cannot continue J4 without a project"; return 1; }

  # --- J4-S2: trigger a run ------------------------------------------------
  local rc rid rcode
  rc="$(create_run "$pid" "add a HELLO line to the repo")"
  rid="${rc%%$'\t'*}"; rcode="${rc##*$'\t'}"
  assert_eq J-MR "POST /runs returns 201" "201" "$rcode"
  assert_nonempty J-MR "created run has id" "$rid"
  [ -n "$rid" ] || { fail J-MR "cannot continue J4 without a run"; return 1; }
  info "J4 run id: $rid"

  # --- J4-S3: run reaches succeeded ----------------------------------------
  local final; final="$(wait_terminal "$rid")"
  assert_eq J-MR "run terminal status is succeeded" "succeeded" "$final"

  # --- J4-S4: event stream carries a run.artifact(bundle) event ------------
  # M3 contract inversion: the runner no longer pushes and no longer emits a
  # run.git event — it uploads a git BUNDLE, which the orchestrator pushes on the
  # user's behalf. The bundle upload surfaces as a run.artifact event kind=bundle.
  local ev_json
  ev_json="$(list_events "$rid")"
  if printf '%s' "$ev_json" | jq -e 'map(select(.type=="run.artifact" and .payload.kind=="bundle"))|length>0' >/dev/null; then
    pass J-MR "event stream contains a run.artifact(bundle) event (runner uploaded a bundle)"
  else
    fail J-MR "no run.artifact(bundle) event in the stream (runner did not upload a bundle)"
  fi

  # --- J4-S5: run.pr_url becomes non-empty (orchestrator pushed + opened PR) -
  # The reconciler pushes the bundle's branch and opens the draft PR AFTER
  # success, so poll the run for pr_url + git_branch (set by the orchestrator).
  local pr_url pr_number branch i
  pr_url=""
  for i in $(seq 1 30); do
    local rj; rj="$(get_run "$rid")"
    pr_url="$(printf '%s' "$rj" | jq -r '.pr_url // empty')"
    pr_number="$(printf '%s' "$rj" | jq -r '.pr_number // empty')"
    branch="$(printf '%s' "$rj" | jq -r '.git_branch // empty')"
    [ -n "$pr_url" ] && break
    sleep 2
  done
  assert_nonempty J-MR "run.pr_url is non-empty (orchestrator opened the draft PR)" "$pr_url"
  assert_nonempty J-MR "run.pr_number is set" "$pr_number"
  # The orchestrator-pushed branch is namespaced jcode/run-* (blueprint §3).
  assert_contains J-MR "run.git_branch is namespaced jcode/run-*" "$branch" "jcode/run-"

  # --- J4-S6: Gitea shows the PR — draft, head=branch, base=main, NOT merged
  if [ -n "$pr_number" ] && [ "$pr_number" != "null" ] && [ "$pr_number" != "0" ]; then
    local pr_json head_ref base_ref merged title
    pr_json="$(gitea_api GET "/repos/$GITEA_ORG/$GITEA_REPO/pulls/$pr_number")"
    head_ref="$(printf '%s' "$pr_json" | jq -r '.head.ref // empty')"
    base_ref="$(printf '%s' "$pr_json" | jq -r '.base.ref // empty')"
    merged="$(printf '%s' "$pr_json" | jq -r '.merged // false')"
    title="$(printf '%s' "$pr_json" | jq -r '.title // empty')"
    assert_eq J-MR "Gitea PR head == orchestrator-pushed branch" "$branch" "$head_ref"
    assert_eq J-MR "Gitea PR base == main" "main" "$base_ref"
    assert_eq J-MR "Gitea PR is NOT merged (never auto-merge)" "false" "$merged"
    # Draft is signalled by Gitea's WIP title prefix.
    assert_contains J-MR "Gitea PR is a draft (WIP title prefix)" "$title" "WIP:"
  else
    fail J-MR "no usable pr_number to verify against Gitea (got '$pr_number')"
  fi

  # --- J4-S7: the diff artifact is STILL present (additive to IN-8) --------
  local art_resp art_code art_content
  art_resp="$(api_get_code "/runs/$rid/artifact")"
  art_code="$(http_code "$art_resp")"
  assert_eq J-MR "GET /runs/{id}/artifact returns 200 (diff still produced)" "200" "$art_code"
  art_content="$(http_body "$art_resp" | jq -r '.content // empty')"
  assert_nonempty J-MR "diff artifact content is non-empty" "$art_content"
}

# Standalone execution.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  j4_run
  print_summary 2>/dev/null || exit 1
fi
