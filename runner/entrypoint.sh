#!/usr/bin/env bash
# entrypoint.sh — run ONE headless jcode task (agent or review) inside the
# container. It is CREDENTIAL-FREE: the runner never holds a provider token
# (blueprint §0/§3). Reading and writing the repo go THROUGH the orchestrator
# control plane over the per-run RUN_TOKEN.
#
# It is fully non-interactive: it drives `jcode acp` (JSON-RPC over stdio) via
# the `acpdrive` client. There is NO TTY.
#
# Required env:
#   TASK_PROMPT      the coding task (agent runs) — a review prompt is built
#                    internally for review runs.
#   MODEL_BASE_URL   OpenAI-compatible base URL (or set START_MOCKLLM=1). Under
#                    the control plane this is the orchestrator's LLM REVERSE
#                    PROXY (.../internal/v1/runs/$RUN_ID/llm, without /v1); the
#                    entrypoint appends /v1 below. The REAL key lives only in the
#                    orchestrator and is injected at forward time (Feature D).
#   MODEL_NAME       "provider/model" id (or set START_MOCKLLM=1) — NO mock default
#   MODEL_API_KEY    API key for the base. Under the control plane this IS the
#                    RUN_TOKEN (the proxy authenticates it); a dummy value is fine
#                    for the mock.
#
# Runner contract env (blueprint §3), injected by the orchestrator:
#   RUN_KIND         "agent" (default) | "review"
#   SOURCE_MODE      "clone" (default) | "fetch"
#                    - clone: `git clone $REPO_URL` (public / raw repos, native
#                      protocol, no credential).
#                    - fetch: download a source bundle from the orchestrator
#                      (GET /internal/v1/runs/$RUN_ID/source, RUN_TOKEN) and clone
#                      it locally — a PRIVATE repo is read WITHOUT any token in
#                      the pod.
#   REPO_URL         clone origin (SOURCE_MODE=clone only)
#   BASE_BRANCH      the baseline branch to check out (may be "")
#   GIT_MODE         "readonly" (default; diff-only) | "draft_pr"
#   BRANCH_NAME      the branch to create for a draft_pr bundle (jcode/run-<id>)
#   PR_HEAD/PR_BASE  review run: the branches to diff (base...head)
#
# Orchestrator wiring (present under the control plane; absent standalone):
#   RUN_ID, RUN_TOKEN, ORCH_BASE_URL
#
# Optional: RUN_TIMEOUT, MODEL_PROVIDER, START_MOCKLLM, MOCK_SCENARIO,
#   WORKSPACE, OUT_DIR.
#   PERSISTENT_WORKSPACE  "1" => /workspace + $HOME/.jcode are a per-service PVC
#                         that survives across runs (Feature C / D05): an existing
#                         checkout is reused (fetch + reset, not re-clone) and
#                         jcode memory persists. Default "0" (ephemeral clone).
#   RUN_SESSION           "1" => multi-turn session mode (F7a / D22, see
#                         docs/14-cloud-v2-design.md §3): instead of driving one
#                         session/prompt and exiting, acpdrive loops — after each
#                         turn it runs runner/turn-hook.sh (this file's former
#                         steps 6/6b/7, extracted so the SAME diff/commit/bundle
#                         logic backs every turn) and long-polls the orchestrator
#                         (RUN_TOKEN) for the next user message on the SAME ACP
#                         session, never re-opening it. Ignored for RUN_KIND=review
#                         (review runs are always single-shot). Default "0"
#                         (single-shot; behavior is then EXACTLY as before F7a).
#
# Output:
#   - agent runs: the git diff on STDOUT (between markers) + /out/diff.patch, and
#     (draft_pr) a git bundle POSTed to the orchestrator (which pushes + opens the
#     draft PR). The runner NEVER pushes.
#   - review runs: REVIEW.md POSTed to the orchestrator.
#   - exit 0 on success; non-zero with a readable error otherwise.

set -euo pipefail

# SCRIPT_DIR locates turn-hook.sh (F7a / D22) relative to this script rather
# than a hardcoded absolute path, so entrypoint.sh keeps working both inside
# the runner image (both files land in /usr/local/bin) and when run directly
# from a repo checkout (e.g. local dev/testing).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOOK_SCRIPT="$SCRIPT_DIR/turn-hook.sh"

log()  { printf '[entrypoint] %s\n' "$*" >&2; }

# report_failure REASON MESSAGE — best-effort POST a run.failure event so the
# console shows a precise failure reason. No-op standalone. Never itself fatal.
report_failure() {
  local reason="$1" message="$2"
  if command -v orchclient >/dev/null 2>&1; then
    orchclient report-failure --reason "$reason" --message "$message" || true
  fi
}

# Note: report_result (POST run.result{outcome}, D18) moved to turn-hook.sh
# (F7a / D22) along with the diff/commit/bundle logic it's paired with — see
# that file's own copy of this helper. Nothing in entrypoint.sh itself needs
# it anymore (the finalize call at the bottom of this file execs turn-hook.sh
# as an independent subprocess, which has no access to shell functions
# defined here anyway).

# die REASON MESSAGE — report the failure (if wired) then exit non-zero.
# REASON ∈ {clone_failed, setup_failed, agent_error, timeout} (docs/11-api.md §1.4).
die() {
  local reason message
  if [ "$#" -ge 2 ]; then
    reason="$1"; message="$2"
  else
    reason="agent_error"; message="$1"
  fi
  printf '[entrypoint] ERROR: %s\n' "$message" >&2
  report_failure "$reason" "$message"
  exit 1
}

RUN_ID="${RUN_ID:-run-$(date +%s)-$$}"
WORKSPACE="${WORKSPACE:-/workspace}"
OUT_DIR="${OUT_DIR:-/out}"
RUN_TIMEOUT="${RUN_TIMEOUT:-300s}"
RUN_KIND="${RUN_KIND:-agent}"
SOURCE_MODE="${SOURCE_MODE:-clone}"
GIT_MODE="${GIT_MODE:-readonly}"
# RUN_SESSION=1 (F7a / D22): multi-turn session mode, see the header comment
# above. Forced off for RUN_KIND=review below (once RUN_KIND is known).
RUN_SESSION="${RUN_SESSION:-0}"
# PERSISTENT_WORKSPACE=1 (Feature C / D05): /workspace and $HOME/.jcode are backed
# by a per-service PVC that survives across runs, so an existing checkout is reused
# (fetch + reset) and jcode memory persists. Default 0 = ephemeral (clone fresh).
PERSISTENT_WORKSPACE="${PERSISTENT_WORKSPACE:-0}"
# BASE_BRANCH is the new contract name; REPO_BRANCH is accepted as a back-compat
# alias for the clone path.
BASE_BRANCH="${BASE_BRANCH:-${REPO_BRANCH:-}}"
# MODEL_NAME has NO silent mock default (fail-visible red line): a real run must
# be told which model to use. The bundled mock rig (START_MOCKLLM=1) sets it
# explicitly below; otherwise it is required (validated in step 1).
MODEL_NAME="${MODEL_NAME:-}"
MODEL_API_KEY="${MODEL_API_KEY:-dummy-key}"

log "run_id=$RUN_ID kind=$RUN_KIND source_mode=$SOURCE_MODE git_mode=$GIT_MODE"

# Defense in depth against cross-run hook execution (Feature C security). A
# persistent workspace PVC can carry .git/hooks planted by a prior run's agent;
# the next run's git checkout/fetch would then trigger that hook — executing
# attacker-controlled code as the runner (which holds RUN_TOKEN). core.hooksPath
# set to /dev/null makes git look for hooks in an empty directory: none can fire.
# Set GLOBALLY (not per-call) so EVERY git invocation in this script AND the
# agent's own git tool calls are covered.
git config --global core.hooksPath /dev/null 2>/dev/null || true

# --- 0. Optional self-contained model: start the bundled mock LLM ------------
MOCK_PID=""
if [ "${START_MOCKLLM:-0}" = "1" ]; then
  log "starting bundled mockllm (scenario=${MOCK_SCENARIO:-write_file})"
  MOCK_ADDR=":8081" MOCK_SCENARIO="${MOCK_SCENARIO:-write_file}" mockllm >&2 &
  MOCK_PID=$!
  MODEL_BASE_URL="http://127.0.0.1:8081/v1"
  # The mock rig is the ONLY place a mock model id is an acceptable default.
  MODEL_NAME="${MODEL_NAME:-mock/mock-model}"
  for _ in $(seq 1 50); do
    if (exec 3<>/dev/tcp/127.0.0.1/8081) 2>/dev/null; then
      exec 3>&- 3<&-
      break
    fi
    sleep 0.1
  done
fi
trap '[ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null || true' EXIT

# --- 1. Validate inputs ------------------------------------------------------
[ -n "${TASK_PROMPT:-}" ]    || die setup_failed "TASK_PROMPT is required"
[ -n "${MODEL_BASE_URL:-}" ] || die setup_failed "MODEL_BASE_URL is required (or set START_MOCKLLM=1)"
[ -n "${MODEL_NAME:-}" ]     || die setup_failed "MODEL_NAME is required (or set START_MOCKLLM=1)"

MODEL_PROVIDER="${MODEL_PROVIDER:-${MODEL_NAME%%/*}}"
MODEL_ID="${MODEL_NAME#*/}"
[ "$MODEL_PROVIDER" != "$MODEL_NAME" ] || die setup_failed "MODEL_NAME must be in 'provider/model' form (got '$MODEL_NAME')"

# reuse_persistent_workspace refreshes an EXISTING checkout on the per-service
# persistent PVC to the latest source and hard-resets it to BASE_BRANCH, instead
# of re-cloning (Feature C / D05). Pollution protection: it first drops any
# leftover work from the prior run (`git reset --hard` + `git clean -fdx`) to a
# clean baseline, and removes any hooks planted in .git/hooks by a prior run
# (cross-run hook execution guard — see core.hooksPath above). This is scoped to
# $WORKSPACE; the jcode memory HOME ($HOME/.jcode) is a SEPARATE mount and is
# deliberately preserved, so memory survives across runs. Returns non-zero on any
# failure so the caller falls back to a clean clone.
reuse_persistent_workspace() {
  local origin_url
  # Security: purge any hooks a prior run's agent may have planted. Belt-and-
  # braces with the global core.hooksPath=/dev/null above — this physically
  # removes the files so they cannot fire even through a path that bypasses the
  # config (e.g. a future git call without the -c flag).
  rm -rf "$WORKSPACE/.git/hooks" 2>/dev/null || true
  if [ "$SOURCE_MODE" = "fetch" ]; then
    # Fresh source bundle from the orchestrator (no credential in the pod). The
    # prior run's origin pointed at a now-gone /tmp bundle, so re-point it.
    [ -n "${ORCH_BASE_URL:-}" ] && [ -n "${RUN_TOKEN:-}" ] || return 1
    SRC_BUNDLE="/tmp/source-$RUN_ID.bundle"
    orchclient fetch-source --out "$SRC_BUNDLE" || return 1
    origin_url="$SRC_BUNDLE"
  else
    [ -n "${REPO_URL:-}" ] || return 1
    origin_url="$REPO_URL"
  fi

  # Pristine baseline: discard uncommitted changes + untracked files from the
  # previous run. $HOME/.jcode (memory) is a different mount, untouched here.
  git -C "$WORKSPACE" reset --hard -q 2>/dev/null || true
  git -C "$WORKSPACE" clean -fdx -q 2>/dev/null || true

  # Point origin at the fresh source and fetch every head into origin/*.
  if git -C "$WORKSPACE" remote get-url origin >/dev/null 2>&1; then
    git -C "$WORKSPACE" remote set-url origin "$origin_url" || return 1
  else
    git -C "$WORKSPACE" remote add origin "$origin_url" || return 1
  fi
  git -C "$WORKSPACE" fetch -q origin "+refs/heads/*:refs/remotes/origin/*" 2>/dev/null || return 1

  # Sync to the target base branch tip — a clean checkout of the latest state.
  if [ -n "$BASE_BRANCH" ]; then
    git -C "$WORKSPACE" checkout -q -B "$BASE_BRANCH" "origin/$BASE_BRANCH" 2>/dev/null \
      || git -C "$WORKSPACE" checkout -q "$BASE_BRANCH" 2>/dev/null || return 1
    git -C "$WORKSPACE" reset --hard -q "origin/$BASE_BRANCH" 2>/dev/null || true
  fi
  return 0
}

# --- 2. Prepare the workspace (clone/fetch fresh, or reuse the persistent PVC) -
# Two shapes:
#   * Ephemeral (default): /workspace starts empty and we clone (or fetch+clone a
#     source bundle) fresh — exactly the J1-J3 behaviour.
#   * Persistent (PERSISTENT_WORKSPACE=1, Feature C / D05): /workspace is a
#     per-service PVC that survives across runs. If it already holds a git
#     checkout we REUSE it (fetch latest + hard-reset to base) instead of
#     re-cloning; the jcode memory HOME ($HOME/.jcode) is a separate subPath, so
#     it is preserved across runs.
mkdir -p "$WORKSPACE"

CLONE_ERR="$(mktemp 2>/dev/null || echo /tmp/git-clone.err)"

WORKSPACE_REUSED=0
if [ "$PERSISTENT_WORKSPACE" = "1" ] && git -C "$WORKSPACE" rev-parse --git-dir >/dev/null 2>&1; then
  # An existing checkout on the persistent PVC: refresh + reset in place.
  if reuse_persistent_workspace; then
    WORKSPACE_REUSED=1
    log "persistent workspace: reused existing checkout (fetched latest, no clone)"
  else
    log "persistent workspace: reuse failed — wiping and cloning fresh"
  fi
fi

if [ "$WORKSPACE_REUSED" = "0" ]; then
  if [ "$PERSISTENT_WORKSPACE" = "1" ]; then
    # Persistent PVC but no usable checkout (first run, or a foreign/corrupt
    # tree): wipe its contents so the clone into an empty dir succeeds. This does
    # NOT touch $HOME/.jcode (a different mount) — memory is kept even on a reclone.
    find "$WORKSPACE" -mindepth 1 -maxdepth 1 -exec rm -rf {} + 2>/dev/null || true
  elif [ -n "$(ls -A "$WORKSPACE" 2>/dev/null || true)" ]; then
    # Ephemeral contract: the workspace MUST start empty (unchanged guard).
    die setup_failed "workspace must be empty"
  fi

  if [ "$SOURCE_MODE" = "fetch" ]; then
    # Private/provider repos: the orchestrator pre-clones and serves a git bundle.
    # No credential ever enters the pod (blueprint §3). fetch-source is load-bearing.
    [ -n "${ORCH_BASE_URL:-}" ] && [ -n "${RUN_TOKEN:-}" ] \
      || die setup_failed "SOURCE_MODE=fetch requires ORCH_BASE_URL + RUN_TOKEN"
    SRC_BUNDLE="/tmp/source-$RUN_ID.bundle"
    log "fetching source bundle from orchestrator"
    orchclient fetch-source --out "$SRC_BUNDLE" \
      || die clone_failed "could not fetch the source bundle from the orchestrator"
    log "cloning source bundle -> $WORKSPACE"
    git clone --quiet "$SRC_BUNDLE" "$WORKSPACE" 2>"$CLONE_ERR" \
      || die clone_failed "git clone of the source bundle failed: $(tr '\n' ' ' < "$CLONE_ERR" | tail -c 500)"
    # Keep $SRC_BUNDLE on disk: the clone made it the `origin` remote, so review
    # runs can `git fetch origin <PR refs>` from it. It lives in the ephemeral pod
    # /tmp and is discarded with the pod.
  else
    # Public / raw repos: clone the URL directly (native protocol, no credential).
    [ -n "${REPO_URL:-}" ] || die setup_failed "SOURCE_MODE=clone requires REPO_URL"
    if [ -n "$BASE_BRANCH" ]; then
      log "cloning $REPO_URL (branch $BASE_BRANCH) -> $WORKSPACE"
      git clone --quiet --branch "$BASE_BRANCH" "$REPO_URL" "$WORKSPACE" 2>"$CLONE_ERR" \
        || die clone_failed "git clone of $REPO_URL (branch $BASE_BRANCH) failed: $(tr '\n' ' ' < "$CLONE_ERR" | tail -c 500)"
    else
      log "cloning $REPO_URL (default branch) -> $WORKSPACE"
      git clone --quiet "$REPO_URL" "$WORKSPACE" 2>"$CLONE_ERR" \
        || die clone_failed "git clone of $REPO_URL failed: $(tr '\n' ' ' < "$CLONE_ERR" | tail -c 500)"
    fi
  fi
fi
rm -f "$CLONE_ERR" 2>/dev/null || true

# Ensure a stable git identity for diffs/commits inside the container.
git -C "$WORKSPACE" config user.email "runner@jcode.local"
git -C "$WORKSPACE" config user.name  "jcode runner"

# Check out the baseline branch when it is not already HEAD (fresh clone path:
# fetch clones the bundle's default HEAD; a specific BASE_BRANCH may be a
# remote-tracking ref). The reuse path already checked out + reset to BASE_BRANCH.
if [ "$WORKSPACE_REUSED" = "0" ] && [ -n "$BASE_BRANCH" ]; then
  if ! git -C "$WORKSPACE" rev-parse --verify -q "refs/heads/$BASE_BRANCH" >/dev/null 2>&1; then
    git -C "$WORKSPACE" checkout -q -B "$BASE_BRANCH" "origin/$BASE_BRANCH" 2>/dev/null \
      || git -C "$WORKSPACE" checkout -q "$BASE_BRANCH" 2>/dev/null || true
  else
    git -C "$WORKSPACE" checkout -q "$BASE_BRANCH" 2>/dev/null || true
  fi
fi

BASE_REF="$(git -C "$WORKSPACE" rev-parse HEAD 2>/dev/null || echo '')"
log "workspace ready at base commit ${BASE_REF:-<none>} (reused=$WORKSPACE_REUSED)"

# turn-hook.sh (F7a / D22) runs as an INDEPENDENT subprocess (execed directly
# by acpdrive in session mode, and by this script below), so it shares no
# shell state with entrypoint.sh — everything it needs must be in its process
# environment. Some of these (WORKSPACE, OUT_DIR, BASE_BRANCH) may have just
# been LOCALLY DEFAULTED above rather than injected by the orchestrator, which
# in bash means they are NOT automatically exported; BASE_REF is always
# locally computed. Export explicitly rather than relying on each var's
# incidental export history.
export WORKSPACE OUT_DIR RUN_ID RUN_KIND GIT_MODE BRANCH_NAME BASE_BRANCH BASE_REF TASK_PROMPT ORCH_BASE_URL RUN_TOKEN

# turn-hook.sh's cross-turn state (per-turn dedup diff + bundle-uploaded
# marker, see its header comment) lives under $WORKSPACE/.git/ so it survives
# across turns WITHIN one run — but on a PERSISTENT_WORKSPACE (Feature C)
# stale state from a PRIOR, unrelated run could otherwise leak in and cause
# this run's first real diff to be silently skipped as a "duplicate", or its
# legitimate no_changes report to be suppressed by another run's bundle
# marker. Always start clean, exactly like the .git/hooks purge above.
rm -f "$WORKSPACE/.git/jcode-turn-hook.last-diff" \
      "$WORKSPACE/.git/jcode-bundle-uploaded" 2>/dev/null || true

# The turn hook is load-bearing for every agent run (per-turn in session mode,
# finalize in both modes): fail fast with a typed reason if it is missing or
# not executable, rather than a bare 127 from acpdrive's hook subprocess (or
# from this script's own set -e at the finalize call) with no precise
# run.failure reason. Review runs never invoke it (they exit at step 5).
if [ "$RUN_KIND" != "review" ]; then
  [ -x "$HOOK_SCRIPT" ] || die setup_failed "turn hook missing or not executable: $HOOK_SCRIPT"
fi

# Session mode is only meaningful for agent runs (a review run's single
# headless turn already exits at step 5, before turn-hook.sh is ever reached);
# force it off defensively for RUN_KIND=review so a misconfigured RUN_SESSION
# can never change review's single-shot contract.
SESSION_MODE=0
if [ "$RUN_SESSION" = "1" ]; then
  if [ "$RUN_KIND" = "review" ]; then
    log "RUN_SESSION=1 ignored for RUN_KIND=review (review runs are always single-shot)"
  else
    SESSION_MODE=1
  fi
fi

# --- 2b. Review runs: build the review prompt from the PR diff ---------------
# The review prompt embeds `git diff PR_BASE...PR_HEAD` and asks the agent to
# write REVIEW.md. It contains the literal marker "[review]" so the mock LLM (and
# any prompt-routing) can identify a review turn.
if [ "$RUN_KIND" = "review" ]; then
  [ -n "${PR_HEAD:-}" ] && [ -n "${PR_BASE:-}" ] \
    || die setup_failed "RUN_KIND=review requires PR_HEAD and PR_BASE"
  log "review run: diffing $PR_BASE...$PR_HEAD"
  # Make sure both refs are present (a bundle clone exposes them as origin/*).
  git -C "$WORKSPACE" fetch -q origin \
    "+refs/heads/$PR_BASE:refs/remotes/origin/$PR_BASE" \
    "+refs/heads/$PR_HEAD:refs/remotes/origin/$PR_HEAD" 2>/dev/null || true
  HEAD_REF="origin/$PR_HEAD"; BASE_REF_R="origin/$PR_BASE"
  git -C "$WORKSPACE" rev-parse --verify -q "$HEAD_REF" >/dev/null 2>&1 || HEAD_REF="$PR_HEAD"
  git -C "$WORKSPACE" rev-parse --verify -q "$BASE_REF_R" >/dev/null 2>&1 || BASE_REF_R="$PR_BASE"
  REVIEW_DIFF="$(git -C "$WORKSPACE" --no-pager diff "$BASE_REF_R...$HEAD_REF" 2>/dev/null || true)"
  [ -n "$REVIEW_DIFF" ] || REVIEW_DIFF="(the diff could not be computed; review from the branch names alone)"
  TASK_PROMPT="$(cat <<EOF
[review] You are reviewing a pull request. Base branch: $PR_BASE. Head branch: $PR_HEAD.

Below is the unified diff (git diff $PR_BASE...$PR_HEAD). Review it for correctness,
clarity, missing tests, and risk.

Write your review to a file named REVIEW.md in the repository root, in markdown:
  - Start with a conclusion line: exactly one of "approve" or "needs-work".
  - Then a bulleted list of specific, actionable findings.

=== DIFF START ===
$REVIEW_DIFF
=== DIFF END ===
EOF
)"
fi

# --- 3. Write jcode config pointing at the model -----------------------------
# config.json is REWRITTEN every run (the model config can change between runs).
# jcode's memory files live ELSEWHERE under $HOME/.jcode and are NOT touched here,
# so with the persistent HOME mount (Feature C) existing project/global memory is
# preserved and reused. Memory is enabled only in persistent mode — an ephemeral
# HOME would discard it anyway, so keep the pre-Feature-C default (disabled) off.
#
# Session transcript hygiene (D12 known boundary): jcode's session.Recorder also
# writes under $HOME/.jcode (sessions/{uuid}.json per docs/01-architecture.md).
# Raw transcripts contain the full prompt (and possibly secrets/PII from the
# repo). The same service may be triggered by users of different trust levels
# (an internal member vs an external @jcode contributor on a shared PR). To avoid
# a prior run's transcript leaking to the next run's operator, scrub the sessions
# directory before each run while preserving the memory/ directory (D12 wants
# memory persisted, NOT raw transcripts — those go to the control-plane store).
MEMORY_ENABLED=false
[ "$PERSISTENT_WORKSPACE" = "1" ] && MEMORY_ENABLED=true
mkdir -p "$HOME/.jcode"
if [ "$PERSISTENT_WORKSPACE" = "1" ] && [ -d "$HOME/.jcode/sessions" ]; then
  rm -rf "$HOME/.jcode/sessions" 2>/dev/null || true
fi
# Feature D — normalize MODEL_BASE_URL so jcode always sees a /v1-terminated base.
# jcode (OpenAI-compatible client) treats base_url as ALREADY including /v1 and
# appends a relative path like /chat/completions. Under the control plane the
# orchestrator injects the LLM PROXY url (.../llm, WITHOUT /v1) as MODEL_BASE_URL;
# standalone (START_MOCKLLM or a direct base) sets it with /v1. Append /v1 unless
# it already ends in /v1 (drop any trailing slash first), so both shapes compose
# the same way and the proxy's transparent forwarding stays correct. The proxy
# strips the matching /v1 from the REAL model base and re-attaches this /v1, so
# there is never a double /v1 regardless of how the admin configured the base.
MODEL_BASE_URL="${MODEL_BASE_URL%/}"
case "$MODEL_BASE_URL" in
  */v1) : ;;                           # already /v1-terminated — keep as-is
  *)    MODEL_BASE_URL="$MODEL_BASE_URL/v1" ;;
esac
cat > "$HOME/.jcode/config.json" <<JSON
{
  "providers": {
    "$MODEL_PROVIDER": {
      "api_key": "$MODEL_API_KEY",
      "base_url": "$MODEL_BASE_URL",
      "custom_models": [
        { "id": "$MODEL_ID", "name": "$MODEL_ID", "tool_call": true, "context": 128000 }
      ]
    }
  },
  "model": "$MODEL_NAME",
  "default_mode": "full_access",
  "memory": { "enabled": $MEMORY_ENABLED }
}
JSON
log "wrote $HOME/.jcode/config.json (provider=$MODEL_PROVIDER model=$MODEL_ID base_url=$MODEL_BASE_URL memory=$MEMORY_ENABLED)"

# Test-only hook: stop right after workspace preparation + config write, BEFORE
# the agent runs. It lets runner/test-persistent-reuse.sh exercise the REAL
# clone-vs-reuse logic + memory flag without a model/jcode binary. It is never set
# in production and cannot fake a success — it exits before any diff/bundle exists.
if [ "${JCLOUD_PREP_ONLY:-0}" = "1" ]; then
  log "JCLOUD_PREP_ONLY=1: exiting after workspace prep + config (test hook)"
  exit 0
fi

# --- 4. Drive one headless jcode run (or a multi-turn session, F7a / D22) ----
# Single-shot (SESSION_MODE=0): acpdrive drives exactly one session/prompt and
# exits — unchanged from before F7a. Session mode (SESSION_MODE=1): acpdrive
# loops, running turn-hook.sh after every turn and long-polling the
# orchestrator for follow-up messages on the same ACP session (see acpdrive
# --session/--turn-hook in runner/acpdrive/session.go). Either way, acpdrive
# only EXITS once the whole run/session is over, so the finalize step below
# (§5 for review, or the diff/report step after it for agent) runs exactly
# once regardless of how many turns happened inside.
log "starting headless run (timeout=$RUN_TIMEOUT session=$SESSION_MODE)"
set +e
if [ "$SESSION_MODE" = "1" ]; then
  JCODE_BIN=jcode acpdrive \
    --workspace "$WORKSPACE" \
    --prompt "$TASK_PROMPT" \
    --timeout "$RUN_TIMEOUT" \
    --session --turn-hook "$HOOK_SCRIPT" \
    --verbose < /dev/null
else
  JCODE_BIN=jcode acpdrive \
    --workspace "$WORKSPACE" \
    --prompt "$TASK_PROMPT" \
    --timeout "$RUN_TIMEOUT" \
    --verbose < /dev/null
fi
RUN_RC=$?
set -e
if [ "$RUN_RC" -eq 124 ]; then
  die timeout "headless agent run exceeded RUN_TIMEOUT=$RUN_TIMEOUT"
elif [ "$RUN_RC" -ne 0 ]; then
  die agent_error "headless agent run failed (rc=$RUN_RC)"
fi
log "headless run finished ok"

# --- 5. Review runs: read + upload REVIEW.md, then done ----------------------
if [ "$RUN_KIND" = "review" ]; then
  REVIEW_FILE="$WORKSPACE/REVIEW.md"
  if [ -s "$REVIEW_FILE" ]; then
    log "review produced REVIEW.md ($(wc -c < "$REVIEW_FILE" | tr -d ' ') bytes)"
    if command -v orchclient >/dev/null 2>&1 && [ -n "${ORCH_BASE_URL:-}" ] && [ -n "${RUN_TOKEN:-}" ]; then
      orchclient post-review --file "$REVIEW_FILE" \
        || log "review upload failed (non-fatal; no review comment will be posted)"
    fi
  else
    die agent_error "review run produced no REVIEW.md"
  fi
  log "success"
  exit 0
fi

# --- 6/6b/7. Diff → (draft_pr) commit+bundle → upload, via turn-hook.sh -----
# This used to be inlined here; it is now runner/turn-hook.sh (F7a / D22) so
# the EXACT same logic backs both this single call and every mid-session turn
# (acpdrive already ran it once per turn via --turn-hook, above, when
# SESSION_MODE=1). Called here with TURN_HOOK_FINALIZE=1: this is either the
# single-shot run's ONE turn (empty diff → report_result no_changes, matching
# pre-F7a behavior exactly), or the session's finalization after acpdrive
# returned 0 (410 from next-prompt) — "no changes across ANY turn" is only
# knowable now, so no_changes is reported here rather than mid-loop (see
# turn-hook.sh's header comment). A non-empty diff at this point in session
# mode was already committed/uploaded by the last per-turn hook call, so the
# turn-hook's own dedup silently no-ops the commit/upload here.
TURN_INDEX=1 ACP_SESSION_ID="" ACP_STOP_REASON="" TURN_HOOK_FINALIZE=1 "$HOOK_SCRIPT"

log "success"
exit 0
