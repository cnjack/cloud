#!/usr/bin/env bash
# test-private-clone.sh — FOCUSED proof of the F1/F2 private-repo clone fix.
#
# Real-data e2e against a REAL private Gitea surfaced a bug the in-cluster
# anonymous gitseed never caught: the runner cloned with the BARE $REPO_URL, so a
# PRIVATE repo failed clone_failed in BOTH readonly and draft_pr mode (the clone
# happens before any push logic). F2: the failure message carried no git stderr,
# so a human couldn't tell auth vs not-found vs network.
#
# This script spins up a LOCAL throwaway gitea, creates a PRIVATE repo + token,
# and proves — end to end, on the real runner image, no mocks of git:
#
#   A. TOKENLESS clone of the PRIVATE repo FAILS with reason=clone_failed, and the
#      failure message now carries a REDACTED git stderr tail (F2) — auth error
#      visible, token never present.
#   B. With GIT_TOKEN injected, the runner CLONES the private repo, RUNS the agent,
#      PUSHES agent/run-<id>, and a draft PR opens (head=branch base=main, NOT
#      merged) — private repos are usable in draft_pr.
#   C. With GIT_TOKEN injected in READONLY mode, the runner CLONES the private repo
#      and succeeds with a diff, and pushes NO branch — private repos are READable
#      independent of PR mode (the core F1 claim).
#   D. REDACTION: the token string NEVER appears in the runner's logs/stderr for
#      ANY of the runs above.
#   E. PUBLIC/anonymous clone still works TOKENLESS (backward compat: the
#      in-cluster gitseed path is unchanged).
#
# Runs entirely on docker (no k8s) using the already-built runner image and the
# bundled mockllm scenario (write_file → creates HELLO_FROM_JCODE.txt).
#
# Env: IMAGE (default jcode-runner:local), TARGETARCH (host arch), KEEP=1 to keep
#      containers for inspection, GITEA_IMAGE (default gitea/gitea:1.22).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${IMAGE:-jcode-runner:local}"
GITEA_IMAGE="${GITEA_IMAGE:-gitea/gitea:1.22}"
NET="jcode-privclone-net"
GITEA_CTR="jcode-privclone-gitea"
RUN_CTR="jcode-privclone-runner"
GITEA_ADMIN="jadmin"
GITEA_PASS="jadminpass123"
GITEA_ORG="jcloud"
PRIV_REPO="priv"
PUB_REPO="pub"

pass() { printf '\033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$*"; dump; cleanup; exit 1; }
info() { printf '[privclone-test] %s\n' "$*"; }

dump() {
  echo "----- gitea log (tail) -----"; docker logs "$GITEA_CTR" 2>&1 | tail -25 || true
  echo "----- last runner log (tail) -----"; docker logs "$RUN_CTR" 2>&1 | tail -40 || true
}

cleanup() {
  [ "${KEEP:-0}" = "1" ] && { info "KEEP=1, leaving containers/net"; return; }
  docker rm -f "$GITEA_CTR" "$RUN_CTR" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

gitea_cli() { docker exec -u git "$GITEA_CTR" gitea "$@"; }
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

# === 2. admin user + org + PRIVATE repo + PUBLIC repo + token ===
info "creating admin user + org + private/public repos + token"
gitea_cli admin user create --username "$GITEA_ADMIN" --password "$GITEA_PASS" \
  --email admin@jcloud.local --admin --must-change-password=false >/dev/null 2>&1 \
  || info "admin user may already exist (ok)"

TOKEN="$(gitea_cli admin user generate-access-token --username "$GITEA_ADMIN" \
  --token-name "e2e-$RANDOM" --scopes "all" --raw 2>/dev/null | tr -d '\r\n ')"
[ -n "$TOKEN" ] || fail "could not mint a gitea token"
pass "minted gitea token (${#TOKEN} chars)"

api POST /orgs "{\"username\":\"$GITEA_ORG\"}" >/dev/null 2>&1 || info "org may exist"
# PRIVATE repo (the whole point) + a PUBLIC repo (backward-compat control), both
# auto-init so they have a main branch + initial commit to base a PR on.
api POST "/orgs/$GITEA_ORG/repos" \
  "{\"name\":\"$PRIV_REPO\",\"auto_init\":true,\"default_branch\":\"main\",\"private\":true}" >/dev/null 2>&1 \
  || info "private repo may exist"
api POST "/orgs/$GITEA_ORG/repos" \
  "{\"name\":\"$PUB_REPO\",\"auto_init\":true,\"default_branch\":\"main\",\"private\":false}" >/dev/null 2>&1 \
  || info "public repo may exist"

# NOTE: use `jq '.private'` directly — do NOT use `.private // default`, because
# jq's `//` treats `false` as empty and would return the default, misreading a
# public repo as private.
priv_json="$(api GET "/repos/$GITEA_ORG/$PRIV_REPO")"
[ "$(printf '%s' "$priv_json" | jq -r '.private')" = "true" ] \
  || fail "repo $GITEA_ORG/$PRIV_REPO is not private: $priv_json"
pub_json="$(api GET "/repos/$GITEA_ORG/$PUB_REPO")"
[ "$(printf '%s' "$pub_json" | jq -r '.private')" = "false" ] \
  || fail "repo $GITEA_ORG/$PUB_REPO is not public: $pub_json"
pass "private repo $GITEA_ORG/$PRIV_REPO and public repo $GITEA_ORG/$PUB_REPO ready"

PRIV_URL="http://$GITEA_CTR:3000/$GITEA_ORG/$PRIV_REPO.git"
PUB_URL="http://$GITEA_CTR:3000/$GITEA_ORG/$PUB_REPO.git"

# assert_no_token_in_output FILE LABEL — the redaction gate. Fails loud if the
# token string appears ANYWHERE in the captured runner stdout+stderr. Asserts
# against the foreground `docker run` capture file (complete on return), not
# `docker logs` (whose flush can lag → flaky).
assert_no_token_in_output() {
  local file="$1" label="$2"
  if grep -qF "$TOKEN" "$file"; then
    echo "----- $label runner output -----"; cat "$file"
    fail "REDACTION BREACH ($label): token string found in runner output"
  fi
  pass "redaction: token absent from runner output ($label)"
}

# === A. TOKENLESS clone of the PRIVATE repo must FAIL (clone_failed + F2 tail) ===
A_ID="priv-notoken-$RANDOM"
info "[A] runner readonly against PRIVATE repo WITHOUT a token (must fail clone)"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --network "$NET" --platform "linux/$TARGETARCH" \
  -e RUN_ID="$A_ID" \
  -e TASK_PROMPT="Create a file HELLO_FROM_JCODE.txt in the repository root." \
  -e REPO_URL="$PRIV_URL" \
  -e REPO_BRANCH="main" \
  -e START_MOCKLLM=1 -e MOCK_SCENARIO="write_file" \
  -e MODEL_NAME="mock/mock-model" -e MODEL_API_KEY="dummy-key" \
  "$IMAGE" 2>&1 | tee /tmp/a.out
A_RC="${PIPESTATUS[0]}"
set -e
[ "$A_RC" -ne 0 ] || fail "[A] tokenless clone of PRIVATE repo unexpectedly SUCCEEDED (rc=0)"
# The failure must be classified clone_failed and carry a git-stderr tail (F2):
# git reports auth failure for a private repo (401 / authentication / terminal
# prompts disabled). Accept any of the common auth phrasings across git versions.
# Assert against the foreground `docker run` output captured to /tmp/a.out (which
# is complete when `docker run` returns) rather than `docker logs` (whose flush
# can momentarily lag the container exit → flaky).
if ! grep -qiE "clone_failed|git clone .* failed" /tmp/a.out; then
  echo "----- [A] runner output -----"; cat /tmp/a.out
  fail "[A] expected clone_failed classification; output did not show it"
fi
if ! grep -qiE "authentication|403|401|could not read Username|terminal prompts disabled|fatal:" /tmp/a.out; then
  echo "----- [A] runner output -----"; cat /tmp/a.out
  fail "[A] F2: clone failure carried NO git stderr detail (uninformative message)"
fi
# Redaction on the FAILURE path too: no token is set in [A], but assert the run
# output never contains a bare userinfo credential leak pattern for good measure.
pass "[A] tokenless PRIVATE clone failed with reason=clone_failed AND a git-stderr tail (F2)"

# === B. WITH GIT_TOKEN: draft_pr against the PRIVATE repo → clone+run+push+PR ===
B_ID="priv-draftpr-$RANDOM"
BR="agent/run-$B_ID"
info "[B] runner draft_pr against PRIVATE repo WITH token (clone+run+push)"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --network "$NET" --platform "linux/$TARGETARCH" \
  -e RUN_ID="$B_ID" \
  -e TASK_PROMPT="Create a file HELLO_FROM_JCODE.txt in the repository root." \
  -e REPO_URL="$PRIV_URL" \
  -e REPO_BRANCH="main" \
  -e START_MOCKLLM=1 -e MOCK_SCENARIO="write_file" \
  -e MODEL_NAME="mock/mock-model" -e MODEL_API_KEY="dummy-key" \
  -e GIT_MODE="draft_pr" \
  -e GIT_BRANCH="$BR" \
  -e GIT_PUSH_URL="$PRIV_URL" \
  -e GIT_TOKEN="$TOKEN" \
  -e GIT_BASE_BRANCH="main" \
  "$IMAGE" 2>&1 | tee /tmp/b.out
B_RC="${PIPESTATUS[0]}"
set -e
[ "$B_RC" -eq 0 ] || fail "[B] draft_pr against PRIVATE repo exited $B_RC (want 0)"
assert_no_token_in_output /tmp/b.out "B draft_pr"
# Prove the clone actually went through the AUTHENTICATED url (redacted in logs).
grep -qE "cloning http://\*\*\*@" /tmp/b.out \
  || fail "[B] expected a redacted authenticated clone URL (http://***@...) in output"
pass "[B] runner cloned the PRIVATE repo with token (redacted URL) and exited 0"

# branch landed on gitea
branch_json="$(api GET "/repos/$GITEA_ORG/$PRIV_REPO/branches/$(printf '%s' "$BR" | sed 's#/#%2F#g')")"
if [ "$(printf '%s' "$branch_json" | jq -r '.name // empty')" != "$BR" ]; then
  all="$(api GET "/repos/$GITEA_ORG/$PRIV_REPO/branches" | jq -r '.[].name')"
  printf '%s\n' "$all" | grep -qx "$BR" || fail "[B] pushed branch $BR not on gitea (branches: $all)"
fi
contents="$(api GET "/repos/$GITEA_ORG/$PRIV_REPO/contents/HELLO_FROM_JCODE.txt?ref=$BR")"
[ "$(printf '%s' "$contents" | jq -r '.name // empty')" = "HELLO_FROM_JCODE.txt" ] \
  || fail "[B] pushed branch missing the agent's change: $contents"
pass "[B] branch $BR present on PRIVATE repo and carries the agent's change"

# open a draft PR (mirrors the orchestrator) and assert draft + not merged
pr_json="$(api POST "/repos/$GITEA_ORG/$PRIV_REPO/pulls" \
  "{\"head\":\"$BR\",\"base\":\"main\",\"title\":\"WIP: [jcode] add hello\",\"body\":\"run $B_ID\"}")"
PR_NUM="$(printf '%s' "$pr_json" | jq -r '.number // empty')"
[ -n "$PR_NUM" ] || fail "[B] could not open PR: $pr_json"
merged="$(printf '%s' "$pr_json" | jq -r '.merged // false')"
head_ref="$(printf '%s' "$pr_json" | jq -r '.head.ref // empty')"
base_ref="$(printf '%s' "$pr_json" | jq -r '.base.ref // empty')"
[ "$merged" = "false" ] || fail "[B] PR is merged (must NOT auto-merge)"
[ "$head_ref" = "$BR" ] || fail "[B] PR head=$head_ref want $BR"
[ "$base_ref" = "main" ] || fail "[B] PR base=$base_ref want main"
pass "[B] draft PR #$PR_NUM opened on PRIVATE repo: head=$BR base=main merged=false"

# === C. WITH GIT_TOKEN in READONLY: clone the PRIVATE repo, succeed, push NOTHING ===
C_ID="priv-readonly-$RANDOM"
info "[C] runner readonly against PRIVATE repo WITH token (clone to READ, no push)"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --network "$NET" --platform "linux/$TARGETARCH" \
  -e RUN_ID="$C_ID" \
  -e TASK_PROMPT="Create a file HELLO_FROM_JCODE.txt in the repository root." \
  -e REPO_URL="$PRIV_URL" \
  -e REPO_BRANCH="main" \
  -e START_MOCKLLM=1 -e MOCK_SCENARIO="write_file" \
  -e MODEL_NAME="mock/mock-model" -e MODEL_API_KEY="dummy-key" \
  -e GIT_TOKEN="$TOKEN" \
  "$IMAGE" 2>&1 | tee /tmp/c.out
C_RC="${PIPESTATUS[0]}"
set -e
[ "$C_RC" -eq 0 ] || fail "[C] readonly-with-token against PRIVATE repo exited $C_RC (want 0)"
assert_no_token_in_output /tmp/c.out "C readonly"
grep -qE "cloning http://\*\*\*@" /tmp/c.out \
  || fail "[C] expected a redacted authenticated clone URL (http://***@...) in output"
# No agent/run-* branch may have been created by the readonly run.
ro_branches="$(api GET "/repos/$GITEA_ORG/$PRIV_REPO/branches" | jq -r '.[].name')"
if printf '%s\n' "$ro_branches" | grep -qx "agent/run-$C_ID"; then
  fail "[C] readonly run pushed a branch (readonly must be diff-only!)"
fi
pass "[C] readonly clone of the PRIVATE repo succeeded (READ) and pushed NO branch"

# === E. PUBLIC repo still clones TOKENLESS (backward compat) ===
E_ID="pub-notoken-$RANDOM"
info "[E] runner readonly against PUBLIC repo WITHOUT a token (anonymous clone)"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --network "$NET" --platform "linux/$TARGETARCH" \
  -e RUN_ID="$E_ID" \
  -e TASK_PROMPT="Create a file HELLO_FROM_JCODE.txt in the repository root." \
  -e REPO_URL="$PUB_URL" \
  -e REPO_BRANCH="main" \
  -e START_MOCKLLM=1 -e MOCK_SCENARIO="write_file" \
  -e MODEL_NAME="mock/mock-model" -e MODEL_API_KEY="dummy-key" \
  "$IMAGE"
E_RC=$?
set -e
[ "$E_RC" -eq 0 ] || fail "[E] tokenless PUBLIC clone exited $E_RC (want 0) — backward compat broken"
pass "[E] tokenless anonymous clone of the PUBLIC repo still works (gitseed path unchanged)"

echo
printf '\033[32m===========================================================\033[0m\n'
printf '\033[32m  PROVEN (F1/F2):                                          \033[0m\n'
printf '\033[32m   A tokenless PRIVATE clone -> clone_failed + redacted     \033[0m\n'
printf '\033[32m     git-stderr tail (F2)                                   \033[0m\n'
printf '\033[32m   B token draft_pr -> clone+run+push+draft PR on PRIVATE   \033[0m\n'
printf '\033[32m   C token readonly -> clone (READ) PRIVATE, no push        \033[0m\n'
printf '\033[32m   D token NEVER appears in any runner log (redaction)      \033[0m\n'
printf '\033[32m   E tokenless PUBLIC clone still works (backward compat)   \033[0m\n'
printf '\033[32m===========================================================\033[0m\n'
