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
#   MODEL_BASE_URL   OpenAI-compatible base URL (or set START_MOCKLLM=1)
#   MODEL_NAME       "provider/model" id (or set START_MOCKLLM=1) — NO mock default
#   MODEL_API_KEY    API key (a dummy value is fine for the mock)
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
#
# Output:
#   - agent runs: the git diff on STDOUT (between markers) + /out/diff.patch, and
#     (draft_pr) a git bundle POSTed to the orchestrator (which pushes + opens the
#     draft PR). The runner NEVER pushes.
#   - review runs: REVIEW.md POSTed to the orchestrator.
#   - exit 0 on success; non-zero with a readable error otherwise.

set -euo pipefail

log()  { printf '[entrypoint] %s\n' "$*" >&2; }

# report_failure REASON MESSAGE — best-effort POST a run.failure event so the
# console shows a precise failure reason. No-op standalone. Never itself fatal.
report_failure() {
  local reason="$1" message="$2"
  if command -v orchclient >/dev/null 2>&1; then
    orchclient report-failure --reason "$reason" --message "$message" || true
  fi
}

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

# --- 4. Drive one headless jcode run -----------------------------------------
log "starting headless run (timeout=$RUN_TIMEOUT)"
set +e
JCODE_BIN=jcode acpdrive \
  --workspace "$WORKSPACE" \
  --prompt "$TASK_PROMPT" \
  --timeout "$RUN_TIMEOUT" \
  --verbose < /dev/null
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

# --- 6. Agent runs: produce the diff -----------------------------------------
git -C "$WORKSPACE" add -N . >/dev/null 2>&1 || true
if [ -n "$BASE_REF" ]; then
  DIFF="$(git -C "$WORKSPACE" --no-pager diff --binary "$BASE_REF")"
else
  log "no BASE_REF recorded; falling back to plain 'git diff' (worktree vs index)"
  DIFF="$(git -C "$WORKSPACE" --no-pager diff --binary)"
fi

mkdir -p "$OUT_DIR" 2>/dev/null || true
if printf '%s\n' "$DIFF" > "$OUT_DIR/diff.patch" 2>/dev/null; then
  log "wrote $OUT_DIR/diff.patch ($(wc -c < "$OUT_DIR/diff.patch" | tr -d ' ') bytes)"
else
  log "could not write $OUT_DIR/diff.patch (continuing; diff still on stdout)"
fi

printf '===JCODE_DIFF_BEGIN run_id=%s===\n' "$RUN_ID"
printf '%s\n' "$DIFF"
printf '===JCODE_DIFF_END run_id=%s===\n' "$RUN_ID"

if [ -z "$DIFF" ]; then
  die agent_error "run produced an empty diff (no changes)"
fi

# --- 6b. Draft-PR bundle (blueprint §3) --------------------------------------
# In draft_pr mode the runner commits the agent's change onto BRANCH_NAME and
# builds a git bundle (BASE_BRANCH..BRANCH_NAME), then POSTs the bundle to the
# orchestrator, which pushes the branch and opens the draft PR on the triggering
# user's behalf. The runner NEVER pushes and holds NO token. readonly mode skips
# this entirely (diff-only, unchanged).
if [ "$GIT_MODE" = "draft_pr" ]; then
  BRANCH_NAME="${BRANCH_NAME:-jcode/run-$RUN_ID}"
  [ -n "$BASE_REF" ] || die setup_failed "draft_pr requires a base commit but none was recorded"

  # Update mode (M7 webhook @mention task): BRANCH_NAME == BASE_BRANCH, i.e. the
  # agent builds ON an existing PR head branch and pushes back to it. We are
  # ALREADY on that branch after the checkout above, so do NOT create a new
  # branch (it exists) — just commit onto it. Otherwise (ordinary draft PR) cut a
  # fresh jcode/run-<id> branch off the base. Either way the bundle below is
  # <start SHA>..HEAD, so the orchestrator reconstructs exactly the new commits.
  if [ -n "$BASE_BRANCH" ] && [ "$BRANCH_NAME" = "$BASE_BRANCH" ]; then
    log "draft_pr update mode: committing agent changes onto existing branch $BRANCH_NAME (base $BASE_REF)"
  else
    log "draft_pr: committing agent changes onto new branch $BRANCH_NAME and bundling"
    git -C "$WORKSPACE" checkout -q -b "$BRANCH_NAME" \
      || die agent_error "could not create branch $BRANCH_NAME"
  fi
  git -C "$WORKSPACE" add -A >/dev/null 2>&1 || true
  if ! git -C "$WORKSPACE" diff --cached --quiet; then
    git -C "$WORKSPACE" commit -q -m "[jcode] ${TASK_PROMPT%%$'\n'*}" \
      || die agent_error "could not commit changes onto $BRANCH_NAME"
  fi

  RUN_BUNDLE="/tmp/run-$RUN_ID.bundle"
  # BASE_REF..BRANCH_NAME → the bundle names refs/heads/BRANCH_NAME with BASE_REF
  # as the prerequisite; the orchestrator's bare clone already has BASE_REF, so
  # `git fetch <bundle>` then `git push` reconstructs the branch.
  git -C "$WORKSPACE" bundle create "$RUN_BUNDLE" "$BASE_REF..$BRANCH_NAME" >/dev/null 2>&1 \
    || die agent_error "could not create the run bundle"

  # Client-side 16MiB self-check (server enforces the same limit with a 413).
  BUNDLE_BYTES="$(wc -c < "$RUN_BUNDLE" | tr -d ' ')"
  if [ "$BUNDLE_BYTES" -gt 16777216 ]; then
    rm -f "$RUN_BUNDLE" 2>/dev/null || true
    die agent_error "run bundle is $BUNDLE_BYTES bytes (>16MiB limit)"
  fi
  log "built run bundle ($BUNDLE_BYTES bytes)"

  if command -v orchclient >/dev/null 2>&1 && [ -n "${ORCH_BASE_URL:-}" ] && [ -n "${RUN_TOKEN:-}" ]; then
    orchclient upload-bundle --file "$RUN_BUNDLE" \
      || log "bundle upload failed (non-fatal; the draft PR will not open until retried)"
  else
    log "no orchestrator wired; skipping bundle upload (standalone run)"
  fi
  rm -f "$RUN_BUNDLE" 2>/dev/null || true
fi

# --- 7. Upload the diff artifact to the orchestrator (best-effort) -----------
if command -v orchclient >/dev/null 2>&1 && [ -n "${ORCH_BASE_URL:-}" ] && [ -n "${RUN_TOKEN:-}" ]; then
  if printf '%s\n' "$DIFF" | orchclient upload-artifact --kind diff --file - ; then
    log "uploaded diff artifact to orchestrator"
  else
    log "diff artifact upload failed (non-fatal; diff still in /out and stdout)"
  fi
fi

log "success"
exit 0
