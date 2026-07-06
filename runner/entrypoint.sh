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
die()  { printf '[entrypoint] ERROR: %s\n' "$*" >&2; exit 1; }

RUN_ID="${RUN_ID:-run-$(date +%s)-$$}"
WORKSPACE="${WORKSPACE:-/workspace}"
OUT_DIR="${OUT_DIR:-/out}"
RUN_TIMEOUT="${RUN_TIMEOUT:-300s}"
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
[ -n "${REPO_URL:-}" ]       || die "REPO_URL is required"
[ -n "${TASK_PROMPT:-}" ]    || die "TASK_PROMPT is required"
[ -n "${MODEL_BASE_URL:-}" ] || die "MODEL_BASE_URL is required (or set START_MOCKLLM=1)"

# Derive the provider key from MODEL_NAME ("provider/model") unless overridden.
MODEL_PROVIDER="${MODEL_PROVIDER:-${MODEL_NAME%%/*}}"
MODEL_ID="${MODEL_NAME#*/}"
[ "$MODEL_PROVIDER" != "$MODEL_NAME" ] || die "MODEL_NAME must be in 'provider/model' form (got '$MODEL_NAME')"

# --- 2. Clone the repo into a clean workspace --------------------------------
if [ -e "$WORKSPACE" ] && [ -n "$(ls -A "$WORKSPACE" 2>/dev/null || true)" ]; then
  log "workspace $WORKSPACE not empty; cloning into a fresh subdir is unsupported in P0"
  die "workspace must be empty"
fi
mkdir -p "$WORKSPACE"
log "cloning $REPO_URL -> $WORKSPACE"
git clone --quiet "$REPO_URL" "$WORKSPACE" || die "git clone failed"

# Ensure a stable git identity so 'git diff' / any commits work in the container.
git -C "$WORKSPACE" config user.email "runner@jcode.local"
git -C "$WORKSPACE" config user.name  "jcode runner"
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
[ "$RUN_RC" -eq 0 ] || die "headless run failed (rc=$RUN_RC)"
log "headless run finished ok"

# --- 5. Produce the diff -----------------------------------------------------
# 'git add -N' stages intents for untracked files so `git diff` includes them.
git -C "$WORKSPACE" add -A -N >/dev/null 2>&1 || true
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
  die "run produced an empty diff (no changes)"
fi

log "success"
exit 0
