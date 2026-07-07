#!/usr/bin/env bash
# j6-webhook.sh — Journey J6 "@mention webhook closed loop" (M7 / blueprint §8):
# a Gitea PR comment `@jcode …` triggers a cloud run, in-cluster, gitea →
# orchestrator direct (no port-forward for the webhook itself; the script only
# polls the orchestrator over BASE).
#
# Asserts (PRD traceability id J-WH):
#   - `@jcode review` on an existing PR  → a kind=review run appears (origin=webhook),
#     reaches succeeded, and its review comment lands on the PR.
#   - `@jcode Add a CONTRIBUTING.md …`   → a kind=agent run appears (origin=webhook),
#     reaches succeeded, the PR head branch gains a NEW commit (update-push mode),
#     and a 🚀 receipt comment is posted on the PR.
#
# Identity is a hard gate (§8): the commenter's Gitea uid must map to a jcloud
# user. This journey therefore performs the Gitea OAuth login for the gitea admin
# (`jcloud-admin`) first — making it the (cluster-admin) jcloud user whose uid the
# webhook resolves — then comments AS that same admin via the Gitea API.
#
# Because the OAuth round trip must match the registered redirect_uri
# (http://localhost:8080/auth/callback/gitea) and hit the browser-facing Gitea
# (http://localhost:3000), this journey expects BOTH `make port-forward` (orch
# :8080) and `make port-forward-gitea` (gitea :3000) to be running. Any missing
# precondition SKIPs (it is a stretch journey, like J4).
#
# Standalone:
#   BASE=http://localhost:8080 ./j6-webhook.sh
# (BASE defaults to http://localhost:8080 so the OAuth redirect_uri matches.)

# BASE must match the OAuth-app redirect_uri origin, so default to :8080 (make
# port-forward), NOT lib.sh's 18080. Set before sourcing lib.sh so ours wins.
: "${BASE:=http://localhost:8080}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
[ -n "${RESULTS_FILE:-}" ] || source "$HERE/lib.sh"

: "${GITEA_INCLUSTER_URL:=http://gitea.jcloud.svc.cluster.local:3000}"
: "${GITEA_EXTERNAL:=http://localhost:3000}"     # browser-facing gitea (make port-forward-gitea)
: "${GITEA_ORG:=jcloud}"
: "${GITEA_REPO:=seed}"
: "${GITEA_SEED_REPO:=${GITEA_INCLUSTER_URL}/${GITEA_ORG}/${GITEA_REPO}.git}"
: "${GITEA_ADMIN_USER:=jcloud-admin}"
: "${GITEA_ADMIN_PASSWORD:=jcloud-admin-pass-123}"
: "${ORCH_WEBHOOK_URL:=http://orchestrator.jcloud.svc.cluster.local:8080/webhooks/gitea}"
: "${WH_POLL:=40}"   # webhook delivery + run-appearance poll iterations (×2s)

# gitea_api METHOD PATH [JSON] — call the Gitea API from a throwaway in-cluster
# pod using the admin token (same shape as j4). Prints the response body.
gitea_api() {
  local method="$1" path="$2" data="${3:-}"
  if [ -n "$data" ]; then
    kubectl --context "$KCTX" -n "$NAMESPACE" run "gitea-api-$RANDOM" \
      --rm -i --restart=Never --image=curlimages/curl:latest --quiet --command -- \
      curl -sS -X "$method" -H "Authorization: token $GITEA_TOKEN" \
      -H 'Content-Type: application/json' -d "$data" \
      "${GITEA_INCLUSTER_URL}/api/v1${path}" 2>/dev/null
  else
    kubectl --context "$KCTX" -n "$NAMESPACE" run "gitea-api-$RANDOM" \
      --rm -i --restart=Never --image=curlimages/curl:latest --quiet --command -- \
      curl -sS -X "$method" -H "Authorization: token $GITEA_TOKEN" \
      "${GITEA_INCLUSTER_URL}/api/v1${path}" 2>/dev/null
  fi
}

# post_pr_comment PR_NUMBER BODY — comment on a PR AS the admin (fires the
# issue_comment webhook Gitea delivers to the orchestrator in-cluster).
post_pr_comment() {
  local n="$1" body="$2"
  gitea_api POST "/repos/$GITEA_ORG/$GITEA_REPO/issues/$n/comments" \
    "$(jq -Rn --arg b "$body" '{body:$b}')" >/dev/null
}

# oauth_login_gitea USER PASS — drive the full Gitea OAuth login against the
# orchestrator and print the minted jcloud session token (empty on any failure).
# It logs into Gitea (CSRF form), starts /auth/login/gitea, grants consent if
# Gitea shows it, and extracts the jcloud_session cookie the callback sets.
oauth_login_gitea() {
  local user="$1" pass="$2"
  local jar; jar="$(mktemp)"
  trap 'rm -f "$jar"' RETURN

  # 1. Gitea login (grab the form _csrf, then POST credentials).
  local page csrf
  page="$(curl -sS -c "$jar" "$GITEA_EXTERNAL/user/login" 2>/dev/null)" || return 0
  csrf="$(printf '%s' "$page" | sed -n 's/.*name="_csrf"[^>]*value="\([^"]*\)".*/\1/p' | head -1)"
  [ -n "$csrf" ] || return 0
  curl -sS -c "$jar" -b "$jar" -o /dev/null \
    --data-urlencode "_csrf=$csrf" \
    --data-urlencode "user_name=$user" \
    --data-urlencode "password=$pass" \
    "$GITEA_EXTERNAL/user/login" 2>/dev/null || return 0

  # 2. Start the orchestrator OAuth flow and follow into Gitea's authorize page.
  #    -L follows: orch 302 → gitea authorize. Auto-approved → callback sets the
  #    session cookie in the jar; consent required → we land on a grant page.
  local authorize
  authorize="$(curl -sS -c "$jar" -b "$jar" -L "$BASE/auth/login/gitea" 2>/dev/null)" || true

  # If a consent (grant) form is shown, submit it, following back to the callback.
  if printf '%s' "$authorize" | grep -q "login/oauth/grant"; then
    local g_csrf g_client g_redirect g_state g_scope
    g_csrf="$(printf '%s' "$authorize" | sed -n 's/.*name="_csrf"[^>]*value="\([^"]*\)".*/\1/p' | head -1)"
    g_client="$(printf '%s' "$authorize" | sed -n 's/.*name="client_id"[^>]*value="\([^"]*\)".*/\1/p' | head -1)"
    g_redirect="$(printf '%s' "$authorize" | sed -n 's/.*name="redirect_uri"[^>]*value="\([^"]*\)".*/\1/p' | head -1)"
    g_state="$(printf '%s' "$authorize" | sed -n 's/.*name="state"[^>]*value="\([^"]*\)".*/\1/p' | head -1)"
    g_scope="$(printf '%s' "$authorize" | sed -n 's/.*name="scope"[^>]*value="\([^"]*\)".*/\1/p' | head -1)"
    curl -sS -c "$jar" -b "$jar" -o /dev/null -L \
      --data-urlencode "_csrf=$g_csrf" \
      --data-urlencode "client_id=$g_client" \
      --data-urlencode "redirect_uri=$g_redirect" \
      --data-urlencode "state=$g_state" \
      --data-urlencode "scope=$g_scope" \
      --data-urlencode "granted=true" \
      "$GITEA_EXTERNAL/login/oauth/grant" 2>/dev/null || return 0
  fi

  # 3. The jcloud_session cookie is the minted session token.
  awk '$6=="jcloud_session"{print $7}' "$jar" | tail -1
}

j6_run() {
  section "J6 · @mention webhook closed loop (M7 / J-WH)"

  # --- Preconditions -------------------------------------------------------
  if ! kubectl --context "$KCTX" -n "$NAMESPACE" get deploy/gitea >/dev/null 2>&1; then
    skip J-WH "Gitea not deployed; @mention webhook is a stretch goal"
    return 0
  fi
  GITEA_TOKEN="$(kubectl --context "$KCTX" -n "$NAMESPACE" get secret gitea-orchestrator \
    -o jsonpath='{.data.GITEA_TOKEN}' 2>/dev/null | base64 -d 2>/dev/null)"
  if [ -z "$GITEA_TOKEN" ]; then
    skip J-WH "gitea-orchestrator token absent (bootstrap not complete)"
    return 0
  fi
  # The org webhook must exist (bootstrap step 8) or nothing will be delivered.
  if ! gitea_api GET "/orgs/$GITEA_ORG/hooks" | jq -e --arg u "$ORCH_WEBHOOK_URL" \
      'map(select(.config.url==$u))|length>0' >/dev/null 2>&1; then
    skip J-WH "org webhook -> orchestrator not configured (bootstrap step 8); re-run make up"
    return 0
  fi
  info "org webhook -> $ORCH_WEBHOOK_URL present"
  # Browser-facing Gitea must be reachable for the OAuth round trip.
  if ! curl -sS -o /dev/null "$GITEA_EXTERNAL/api/healthz" 2>/dev/null; then
    skip J-WH "Gitea not reachable at $GITEA_EXTERNAL (run: make port-forward-gitea)"
    return 0
  fi
  if ! curl -sS -o /dev/null "$BASE/healthz" 2>/dev/null; then
    skip J-WH "orchestrator not reachable at $BASE (run: make port-forward)"
    return 0
  fi

  # --- Identity: OAuth-login jcloud-admin → a jcloud session ---------------
  info "performing Gitea OAuth login for $GITEA_ADMIN_USER (maps its uid to a jcloud user)"
  local session
  session="$(oauth_login_gitea "$GITEA_ADMIN_USER" "$GITEA_ADMIN_PASSWORD")"
  if [ -z "$session" ]; then
    skip J-WH "OAuth login for $GITEA_ADMIN_USER did not complete (need make port-forward + port-forward-gitea, fresh session)"
    return 0
  fi
  # Use the session token as the bearer for the run-polling API calls.
  TOKEN="$session"
  local me; me="$(api_get "/me")"
  local me_uid; me_uid="$(printf '%s' "$me" | jq -r '.user.id // empty')"
  assert_nonempty J-WH "OAuth login mapped $GITEA_ADMIN_USER to a jcloud user" "$me_uid"
  [ -n "$me_uid" ] || { fail J-WH "no jcloud user after OAuth; cannot continue"; return 1; }

  # --- Set up a project + draft_pr service + an existing PR to comment on --
  local proj_resp proj_code pid
  proj_resp="$(api_post_code "/projects" "{\"name\":\"j6-webhook\"}")"
  proj_code="$(http_code "$proj_resp")"
  pid="$(http_body "$proj_resp" | jq -r '.id // empty')"
  assert_eq J-WH "POST /projects returns 201" "201" "$proj_code"
  [ -n "$pid" ] || { fail J-WH "cannot continue without a project"; return 1; }
  register_project "$pid"

  local svc_resp svc_code sid
  svc_resp="$(api_post_code "/projects/$pid/services" \
    "{\"name\":\"default\",\"provider\":\"gitea\",\"owner_name\":\"$GITEA_ORG/$GITEA_REPO\",\"git_mode\":\"draft_pr\",\"default_branch\":\"main\"}")"
  svc_code="$(http_code "$svc_resp")"
  sid="$(http_body "$svc_resp" | jq -r '.id // empty')"
  assert_eq J-WH "POST /projects/{id}/services (draft_pr) returns 201" "201" "$svc_code"
  [ -n "$sid" ] || { fail J-WH "cannot continue without a service"; return 1; }

  local rc rid final
  rc="$(create_run "$sid" "add a HELLO_WEBHOOK line to the repo")"
  rid="${rc%%$'\t'*}"
  assert_nonempty J-WH "seed agent run created" "$rid"
  final="$(wait_terminal "$rid")"
  assert_eq J-WH "seed run reaches succeeded" "succeeded" "$final"

  # Poll for the PR the orchestrator opened.
  local pr_url pr_number branch i
  for i in $(seq 1 30); do
    local rj; rj="$(get_run "$rid")"
    pr_url="$(printf '%s' "$rj" | jq -r '.pr_url // empty')"
    pr_number="$(printf '%s' "$rj" | jq -r '.pr_number // empty')"
    branch="$(printf '%s' "$rj" | jq -r '.git_branch // empty')"
    [ -n "$pr_url" ] && break
    sleep 2
  done
  assert_nonempty J-WH "seed run opened a PR (pr_number)" "$pr_number"
  [ -n "$pr_number" ] && [ "$pr_number" != "null" ] || { fail J-WH "no PR to comment on"; return 1; }
  info "PR #$pr_number head=$branch"

  # ========================================================================
  # J6-S1: `@jcode review` → a webhook review run + a review comment on the PR
  # ========================================================================
  info "posting '@jcode review' on PR #$pr_number"
  post_pr_comment "$pr_number" "@jcode review"

  local review_rid=""
  for i in $(seq 1 "$WH_POLL"); do
    review_rid="$(api_get "/projects/$pid/runs?limit=50" | jq -r \
      '.runs | map(select(.kind=="review" and .origin=="webhook")) | (.[0].id // empty)')"
    [ -n "$review_rid" ] && break
    sleep 2
  done
  assert_nonempty J-WH "@jcode review created a webhook review run" "$review_rid"
  if [ -n "$review_rid" ]; then
    local rfinal; rfinal="$(wait_terminal "$review_rid")"
    assert_eq J-WH "review run reaches succeeded" "succeeded" "$rfinal"
    # The review comment lands on the PR (review_posted_at stamped, or a review
    # shows up via the Gitea API).
    local posted=""
    for i in $(seq 1 "$WH_POLL"); do
      posted="$(get_run "$review_rid" | jq -r '.review_posted_at // empty')"
      [ -n "$posted" ] && break
      sleep 2
    done
    assert_nonempty J-WH "review comment posted to the PR (review_posted_at)" "$posted"
  fi

  # ========================================================================
  # J6-S2: `@jcode <task>` → a webhook agent run that pushes a NEW commit onto
  # the PR head branch + posts a 🚀 receipt.
  # ========================================================================
  local head_sha_before
  head_sha_before="$(gitea_api GET "/repos/$GITEA_ORG/$GITEA_REPO/pulls/$pr_number" | jq -r '.head.sha // empty')"
  info "PR head sha before task: ${head_sha_before:0:12}"

  info "posting '@jcode Add a CONTRIBUTING.md …' on PR #$pr_number"
  post_pr_comment "$pr_number" "@jcode Add a CONTRIBUTING.md with contribution guidelines"

  local task_rid=""
  for i in $(seq 1 "$WH_POLL"); do
    task_rid="$(api_get "/projects/$pid/runs?limit=50" | jq -r \
      --arg h "$branch" \
      '.runs | map(select(.kind=="agent" and .origin=="webhook" and .pr_head_branch==$h)) | (.[0].id // empty)')"
    [ -n "$task_rid" ] && break
    sleep 2
  done
  assert_nonempty J-WH "@jcode task created a webhook agent run" "$task_rid"
  if [ -n "$task_rid" ]; then
    local tfinal; tfinal="$(wait_terminal "$task_rid")"
    assert_eq J-WH "webhook agent run reaches succeeded" "succeeded" "$tfinal"

    # The PR head branch gains a new commit (update-push mode; ff-only).
    local head_sha_after=""
    for i in $(seq 1 "$WH_POLL"); do
      head_sha_after="$(gitea_api GET "/repos/$GITEA_ORG/$GITEA_REPO/pulls/$pr_number" | jq -r '.head.sha // empty')"
      [ -n "$head_sha_after" ] && [ "$head_sha_after" != "$head_sha_before" ] && break
      sleep 2
    done
    if [ -n "$head_sha_after" ] && [ "$head_sha_after" != "$head_sha_before" ]; then
      pass J-WH "PR head branch advanced (new commit ${head_sha_after:0:12} via update-push)"
    else
      fail J-WH "PR head branch did not advance (before=${head_sha_before:0:12} after=${head_sha_after:0:12})"
    fi
  fi

  # A 🚀 receipt comment is present on the PR.
  local comments receipt
  comments="$(gitea_api GET "/repos/$GITEA_ORG/$GITEA_REPO/issues/$pr_number/comments")"
  receipt="$(printf '%s' "$comments" | jq -r 'map(select(.body | test("run started|🚀"))) | length')"
  if [ "${receipt:-0}" -ge 1 ]; then
    pass J-WH "🚀 receipt comment posted on the PR"
  else
    fail J-WH "no 🚀 receipt comment found on the PR"
  fi
}

# Standalone execution.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  j6_run
  print_summary 2>/dev/null || exit 1
fi
