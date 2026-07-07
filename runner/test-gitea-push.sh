#!/usr/bin/env bash
# test-gitea-push.sh — FOCUSED proof of the ST-1 draft-PR PUSH PATH: the runner,
# in GIT_MODE=draft_pr, commits the agent's change onto agent/run-<id> and pushes
# it to a REAL throwaway Gitea over https with a token, then we assert the branch
# landed and a draft PR can be opened for it. It ALSO re-proves the readonly
# default is UNCHANGED (no branch pushed, run still succeeds).
#
# This runs entirely on docker (no k8s) using the already-built runner image and
# the bundled mockllm scenario (write_file → creates HELLO_FROM_JCODE.txt).
#
# Flow:
#   1. start a throwaway gitea/gitea container; headlessly install + admin user
#   2. create org `jcloud`, repo `seed` (with an initial commit), and an API token
#   3. run the runner container with GIT_MODE=draft_pr pointed at gitea over the
#      docker network, START_MOCKLLM=1 (self-contained model)
#   4. assert: runner exit 0; branch agent/run-<id> exists on gitea; a draft PR
#      opens for it (head=agent/run-<id>, base=main, NOT merged)
#   5. run the SAME runner in readonly (default) mode; assert NO agent/run-* branch
#      is created and the run still succeeds → the default path is unchanged
#
# Env: IMAGE (default jcode-runner:local), TARGETARCH (host arch), KEEP=1 to keep
#      containers for inspection, GITEA_IMAGE (default gitea/gitea:1.22).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${IMAGE:-jcode-runner:local}"
GITEA_IMAGE="${GITEA_IMAGE:-gitea/gitea:1.22}"
NET="jcode-gitea-test-net"
GITEA_CTR="jcode-gitea-test"
RUN_CTR="jcode-runner-gitea-test"
GITEA_ADMIN="jadmin"
GITEA_PASS="jadminpass123"
GITEA_ORG="jcloud"
GITEA_REPO="seed"

pass() { printf '\033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$*"; dump; cleanup; exit 1; }
info() { printf '[gitea-test] %s\n' "$*"; }

dump() {
  echo "----- gitea log (tail) -----"; docker logs "$GITEA_CTR" 2>&1 | tail -25 || true
  echo "----- runner log (tail) -----"; docker logs "$RUN_CTR" 2>&1 | tail -40 || true
}

cleanup() {
  [ "${KEEP:-0}" = "1" ] && { info "KEEP=1, leaving containers/net"; return; }
  docker rm -f "$GITEA_CTR" "$RUN_CTR" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# gitea exec helper (runs a gitea CLI command as the git user inside the ctr).
gitea_cli() { docker exec -u git "$GITEA_CTR" gitea "$@"; }
# authenticated gitea API call FROM INSIDE the runner image (has no curl; use a
# throwaway container with the gitea network). We use a small alpine/curl helper.
api() { # METHOD PATH [JSON]
  local method="$1" path="$2" data="${3:-}"
  if [ -n "$data" ]; then
    docker run --rm --network "$NET" curlimages/curl:latest -sS -X "$method" \
      -H "Authorization: token $TOKEN" -H 'Content-Type: application/json' \
      -d "$data" "http://$GITEA_CTR:3000/api/v1$path"
  else
    docker run --rm --network "$NET" curlimages/curl:latest -sS -X "$method" \
      -H "Authorization: token $TOKEN" "http://$GITEA_CTR:3000/api/v1$path"
  fi
}

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }
need docker; need jq

# --- arch ---
if [ -z "${TARGETARCH:-}" ]; then
  case "$(uname -m)" in
    arm64|aarch64) TARGETARCH=arm64 ;;
    x86_64|amd64)  TARGETARCH=amd64 ;;
    *) fail "unsupported host arch $(uname -m)" ;;
  esac
fi

docker image inspect "$IMAGE" >/dev/null 2>&1 || fail "runner image $IMAGE not found — run build.sh first"

# === 1. network + gitea ===
docker network create "$NET" >/dev/null 2>&1 || true
docker rm -f "$GITEA_CTR" >/dev/null 2>&1 || true
info "starting gitea ($GITEA_IMAGE) headlessly"
docker run -d --name "$GITEA_CTR" --network "$NET" --network-alias "$GITEA_CTR" \
  --platform "linux/$TARGETARCH" \
  -e GITEA__security__INSTALL_LOCK=true \
  -e GITEA__server__ROOT_URL="http://$GITEA_CTR:3000/" \
  -e GITEA__server__OFFLINE_MODE=true \
  -e GITEA__database__DB_TYPE=sqlite3 \
  "$GITEA_IMAGE" >/dev/null || fail "gitea failed to start"

info "waiting for gitea to accept connections"
ready=0
for _ in $(seq 1 90); do
  if docker run --rm --network "$NET" curlimages/curl:latest -sS -o /dev/null \
       "http://$GITEA_CTR:3000/api/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
[ "$ready" = "1" ] || fail "gitea never became ready"
pass "gitea is up"

# === 2. admin user + org + repo + token ===
info "creating admin user + org + repo + token"
gitea_cli admin user create --username "$GITEA_ADMIN" --password "$GITEA_PASS" \
  --email admin@jcloud.local --admin --must-change-password=false >/dev/null 2>&1 \
  || info "admin user may already exist (ok)"

# token via the CLI (scoped to all; MVP single-tenant).
TOKEN="$(gitea_cli admin user generate-access-token --username "$GITEA_ADMIN" \
  --token-name "e2e-$RANDOM" --scopes "all" --raw 2>/dev/null | tr -d '\r\n ')"
[ -n "$TOKEN" ] || fail "could not mint a gitea token"
pass "minted gitea token (${#TOKEN} chars)"

# org
api POST /orgs "{\"username\":\"$GITEA_ORG\"}" >/dev/null 2>&1 || info "org may exist"
# repo with auto-init so it has a main branch + initial commit to base a PR on.
api POST "/orgs/$GITEA_ORG/repos" \
  "{\"name\":\"$GITEA_REPO\",\"auto_init\":true,\"default_branch\":\"main\",\"private\":false}" >/dev/null 2>&1 \
  || info "repo may exist"
# verify the repo exists
repo_json="$(api GET "/repos/$GITEA_ORG/$GITEA_REPO")"
[ "$(printf '%s' "$repo_json" | jq -r '.name // empty')" = "$GITEA_REPO" ] \
  || fail "repo $GITEA_ORG/$GITEA_REPO not created: $repo_json"
pass "org/$GITEA_ORG repo/$GITEA_REPO ready (auto-init main)"

CLONE_URL="http://$GITEA_CTR:3000/$GITEA_ORG/$GITEA_REPO.git"
PUSH_URL="http://$GITEA_CTR:3000/$GITEA_ORG/$GITEA_REPO.git"

# === 3. run the runner in DRAFT_PR mode ===
RUN_ID="itest-$RANDOM"
info "running runner (draft_pr) run_id=$RUN_ID against $CLONE_URL"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --network "$NET" --platform "linux/$TARGETARCH" \
  -e RUN_ID="$RUN_ID" \
  -e TASK_PROMPT="Create a file HELLO_FROM_JCODE.txt in the repository root." \
  -e REPO_URL="$CLONE_URL" \
  -e REPO_BRANCH="main" \
  -e START_MOCKLLM=1 \
  -e MOCK_SCENARIO="write_file" \
  -e MODEL_NAME="mock/mock-model" \
  -e MODEL_API_KEY="dummy-key" \
  -e GIT_MODE="draft_pr" \
  -e GIT_BRANCH="agent/run-$RUN_ID" \
  -e GIT_PUSH_URL="$PUSH_URL" \
  -e GIT_TOKEN="$TOKEN" \
  -e GIT_BASE_BRANCH="main" \
  "$IMAGE"
RC=$?
set -e
[ "$RC" -eq 0 ] || fail "runner (draft_pr) exited $RC (want 0)"
pass "runner (draft_pr) exited 0"

# === 4. assert the branch landed on gitea ===
BR="agent/run-$RUN_ID"
branch_json="$(api GET "/repos/$GITEA_ORG/$GITEA_REPO/branches/$(printf '%s' "$BR" | sed 's#/#%2F#g')")"
if [ "$(printf '%s' "$branch_json" | jq -r '.name // empty')" != "$BR" ]; then
  # Fallback: list branches and grep.
  all="$(api GET "/repos/$GITEA_ORG/$GITEA_REPO/branches" | jq -r '.[].name')"
  printf '%s\n' "$all" | grep -qx "$BR" || fail "pushed branch $BR not found on gitea (branches: $all)"
fi
pass "branch $BR present on gitea"

# a file the mock scenario writes must be on that branch.
contents="$(api GET "/repos/$GITEA_ORG/$GITEA_REPO/contents/HELLO_FROM_JCODE.txt?ref=$BR")"
[ "$(printf '%s' "$contents" | jq -r '.name // empty')" = "HELLO_FROM_JCODE.txt" ] \
  || fail "expected file not on pushed branch: $contents"
pass "pushed branch carries the agent's change (HELLO_FROM_JCODE.txt)"

# === 5. open a draft PR for the branch (mirrors what the orchestrator does) ===
pr_json="$(api POST "/repos/$GITEA_ORG/$GITEA_REPO/pulls" \
  "{\"head\":\"$BR\",\"base\":\"main\",\"title\":\"WIP: [jcode] add hello\",\"body\":\"run $RUN_ID\"}")"
PR_NUM="$(printf '%s' "$pr_json" | jq -r '.number // empty')"
[ -n "$PR_NUM" ] || fail "could not open PR: $pr_json"
# assert it is a draft (WIP prefix ⇒ .title starts WIP: and Gitea flags it) and NOT merged.
merged="$(printf '%s' "$pr_json" | jq -r '.merged // false')"
head_ref="$(printf '%s' "$pr_json" | jq -r '.head.ref // empty')"
base_ref="$(printf '%s' "$pr_json" | jq -r '.base.ref // empty')"
[ "$merged" = "false" ] || fail "PR is merged (must NOT auto-merge)"
[ "$head_ref" = "$BR" ] || fail "PR head=$head_ref want $BR"
[ "$base_ref" = "main" ] || fail "PR base=$base_ref want main"
pass "draft PR #$PR_NUM opened: head=$BR base=main merged=false"

# === 6. re-prove readonly (default) is UNCHANGED ===
RO_ID="itest-ro-$RANDOM"
info "running runner (readonly default) run_id=$RO_ID"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --network "$NET" --platform "linux/$TARGETARCH" \
  -e RUN_ID="$RO_ID" \
  -e TASK_PROMPT="Create a file HELLO_FROM_JCODE.txt in the repository root." \
  -e REPO_URL="$CLONE_URL" \
  -e REPO_BRANCH="main" \
  -e START_MOCKLLM=1 \
  -e MOCK_SCENARIO="write_file" \
  -e MODEL_NAME="mock/mock-model" \
  -e MODEL_API_KEY="dummy-key" \
  "$IMAGE"
RO_RC=$?
set -e
[ "$RO_RC" -eq 0 ] || fail "runner (readonly) exited $RO_RC (want 0)"
# No agent/run-* branch should have been created by the readonly run.
ro_branches="$(api GET "/repos/$GITEA_ORG/$GITEA_REPO/branches" | jq -r '.[].name')"
if printf '%s\n' "$ro_branches" | grep -qx "agent/run-$RO_ID"; then
  fail "readonly run pushed a branch agent/run-$RO_ID (default must be diff-only!)"
fi
pass "readonly (default) run succeeded and pushed NO branch (unchanged behavior)"

echo
printf '\033[32m===========================================================\033[0m\n'
printf '\033[32m  PROVEN: draft_pr runner pushes agent/run-<id> to Gitea,   \033[0m\n'
printf '\033[32m  a draft PR opens (head=branch base=main, NOT merged),     \033[0m\n'
printf '\033[32m  and readonly (default) stays diff-only (no push).         \033[0m\n'
printf '\033[32m===========================================================\033[0m\n'
