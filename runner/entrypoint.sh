#!/usr/bin/env bash
# entrypoint.sh — run ONE headless jcode coding task inside the container and
# emit the resulting git diff.
#
# It is fully non-interactive: it drives `jcode acp` (JSON-RPC over stdio) via
# the `acpdrive` client. There is NO TTY. jcode's TUI (bare `jcode` / `jcode -p`)
# is never invoked.
#
# Required env:
#   REPO_URL         git URL (or file:// path) to clone into /workspace
#   TASK_PROMPT      the coding task to give the agent
#   MODEL_BASE_URL   OpenAI-compatible base URL, e.g. http://mockllm:8081/v1
#   MODEL_API_KEY    API key (a dummy value is fine for the mock)
# Optional env:
#   RUN_ID           opaque id echoed into logs/output (default: random)
#   MODEL_NAME       "provider/model" as jcode expects (default: mock/mock-model)
#   MODEL_PROVIDER   provider key used in config (default: derived from MODEL_NAME)
#   RUN_TIMEOUT      hard ceiling for the agent run (default: 300s)
#   START_MOCKLLM    if "1", start the bundled mockllm on 127.0.0.1:8081 and
#                    point MODEL_BASE_URL at it (self-contained mode)
#   MOCK_SCENARIO    scenario name for the bundled mockllm (default: write_file)
#
# Output:
#   - the git diff is printed to STDOUT (between markers) AND written to
#     /out/diff.patch (best-effort; /out may be a mounted volume)
#   - exit 0 on success; non-zero with a readable error otherwise.

set -euo pipefail

log()  { printf '[entrypoint] %s\n' "$*" >&2; }

# report_failure REASON MESSAGE — best-effort POST a run.failure event to the
# orchestrator so the console shows a precise failure reason (clone/setup/agent)
# instead of the cluster's fallback (agent_error). No-op if the orchestrator env
# is absent (standalone runs) or orchclient is missing. Never itself fatal.
report_failure() {
  local reason="$1" message="$2"
  if command -v orchclient >/dev/null 2>&1; then
    orchclient report-failure --reason "$reason" --message "$message" || true
  fi
}

# die REASON MESSAGE — report the failure (if the orchestrator is wired) then
# exit non-zero. REASON ∈ {clone_failed, setup_failed, agent_error}. Two-arg form
# is preferred; a single arg is treated as an agent_error message for back-compat.
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
# MODEL_NAME is "provider/model" as jcode expects; default provider "mock",
# model "mock-model" (matches the bundled mockllm catalog).
MODEL_NAME="${MODEL_NAME:-mock/mock-model}"
MODEL_API_KEY="${MODEL_API_KEY:-dummy-key}"

log "run_id=$RUN_ID"

# --- 0. Optional self-contained model: start the bundled mock LLM ------------
MOCK_PID=""
if [ "${START_MOCKLLM:-0}" = "1" ]; then
  log "starting bundled mockllm (scenario=${MOCK_SCENARIO:-write_file})"
  MOCK_ADDR=":8081" MOCK_SCENARIO="${MOCK_SCENARIO:-write_file}" mockllm >&2 &
  MOCK_PID=$!
  MODEL_BASE_URL="http://127.0.0.1:8081/v1"
  # Wait for the port to accept connections. Uses bash's /dev/tcp so we don't
  # depend on curl/wget being installed in the slim base image.
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
[ -n "${REPO_URL:-}" ]       || die setup_failed "REPO_URL is required"
[ -n "${TASK_PROMPT:-}" ]    || die setup_failed "TASK_PROMPT is required"
[ -n "${MODEL_BASE_URL:-}" ] || die setup_failed "MODEL_BASE_URL is required (or set START_MOCKLLM=1)"

# Derive the provider key from MODEL_NAME ("provider/model") unless overridden.
MODEL_PROVIDER="${MODEL_PROVIDER:-${MODEL_NAME%%/*}}"
MODEL_ID="${MODEL_NAME#*/}"
[ "$MODEL_PROVIDER" != "$MODEL_NAME" ] || die setup_failed "MODEL_NAME must be in 'provider/model' form (got '$MODEL_NAME')"

# --- 2. Clone the repo into a clean workspace --------------------------------
if [ -e "$WORKSPACE" ] && [ -n "$(ls -A "$WORKSPACE" 2>/dev/null || true)" ]; then
  log "workspace $WORKSPACE not empty; cloning into a fresh subdir is unsupported in P0"
  die setup_failed "workspace must be empty"
fi
mkdir -p "$WORKSPACE"
# REPO_BRANCH selects the baseline branch; empty => clone the remote's default.
# (orchestrator injects REPO_BRANCH from project.default_branch, which may be "".)
if [ -n "${REPO_BRANCH:-}" ]; then
  log "cloning $REPO_URL (branch $REPO_BRANCH) -> $WORKSPACE"
  git clone --quiet --branch "$REPO_BRANCH" "$REPO_URL" "$WORKSPACE" \
    || die clone_failed "git clone of $REPO_URL (branch $REPO_BRANCH) failed"
else
  log "cloning $REPO_URL (default branch) -> $WORKSPACE"
  git clone --quiet "$REPO_URL" "$WORKSPACE" \
    || die clone_failed "git clone of $REPO_URL failed"
fi

# Ensure a stable git identity so 'git diff' / any commits work in the container.
git -C "$WORKSPACE" config user.email "runner@jcode.local"
git -C "$WORKSPACE" config user.name  "jcode runner"
# Baseline for the final diff: HEAD at clone time.
BASE_REF="$(git -C "$WORKSPACE" rev-parse HEAD 2>/dev/null || echo '')"
log "cloned at base commit ${BASE_REF:-<none>}"

# --- 3. Write jcode config pointing at the model -----------------------------
# default_mode=full_access → jcode's ApprovalState runs in auto mode and never
# calls back for tool permission (headless-safe). memory disabled to avoid any
# background model calls.
mkdir -p "$HOME/.jcode"
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
  "memory": { "enabled": false }
}
JSON
log "wrote $HOME/.jcode/config.json (provider=$MODEL_PROVIDER model=$MODEL_ID base_url=$MODEL_BASE_URL)"

# --- 4. Drive one headless jcode run -----------------------------------------
# acpdrive spawns `jcode acp` over stdio (no TTY) and blocks until the agent
# loop completes. stdin is closed so nothing can hang waiting for input.
log "starting headless run (timeout=$RUN_TIMEOUT)"
set +e
JCODE_BIN=jcode acpdrive \
  --workspace "$WORKSPACE" \
  --prompt "$TASK_PROMPT" \
  --timeout "$RUN_TIMEOUT" \
  --verbose < /dev/null
RUN_RC=$?
set -e
# acpdrive drives the agent loop AND streams agent.text/tool events to the
# orchestrator as it runs. A non-zero rc means the agent errored / was refused.
[ "$RUN_RC" -eq 0 ] || die agent_error "headless agent run failed (rc=$RUN_RC)"
log "headless run finished ok"

# --- 5. Produce the diff -----------------------------------------------------
# Baseline is HEAD at clone (BASE_REF). 'git add -N .' stages intents for
# untracked files so `git diff` includes newly-created files.
git -C "$WORKSPACE" add -N . >/dev/null 2>&1 || true
DIFF="$(git -C "$WORKSPACE" --no-pager diff)"

mkdir -p "$OUT_DIR" 2>/dev/null || true
if printf '%s\n' "$DIFF" > "$OUT_DIR/diff.patch" 2>/dev/null; then
  log "wrote $OUT_DIR/diff.patch ($(wc -c < "$OUT_DIR/diff.patch" | tr -d ' ') bytes)"
else
  log "could not write $OUT_DIR/diff.patch (continuing; diff still on stdout)"
fi

# Emit the diff to stdout between machine-parseable markers.
printf '===JCODE_DIFF_BEGIN run_id=%s===\n' "$RUN_ID"
printf '%s\n' "$DIFF"
printf '===JCODE_DIFF_END run_id=%s===\n' "$RUN_ID"

if [ -z "$DIFF" ]; then
  die agent_error "run produced an empty diff (no changes)"
fi

# --- 6. Upload the diff artifact to the orchestrator (best-effort) -----------
# On success the console fetches the diff via /api/v1/runs/{id}/artifact; the
# upload triggers an internally-emitted run.artifact event on the SSE stream.
# orchclient is a no-op (exit 0) when the orchestrator env is absent, so this is
# skipped cleanly in standalone runs.
if command -v orchclient >/dev/null 2>&1 && [ -n "${ORCH_BASE_URL:-}" ] && [ -n "${RUN_TOKEN:-}" ]; then
  if printf '%s\n' "$DIFF" | orchclient upload-artifact --kind diff --file - ; then
    log "uploaded diff artifact to orchestrator"
  else
    log "diff artifact upload failed (non-fatal; diff still in /out and stdout)"
  fi
fi

log "success"
exit 0
