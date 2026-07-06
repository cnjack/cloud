#!/usr/bin/env bash
# test-integration.sh — FULL-LOOP proof of the runner <-> orchestrator wiring,
# WITHOUT Kubernetes. It exercises the exact production path end to end:
#
#   REST create project (file:///seed repo) + run
#     -> orchestrator reconciler schedules the runner via the `process`
#        JobLauncher (docker run) with the same env a K8s Job would inject
#     -> runner clones the repo, drives jcode headless against mockllm, and
#        STREAMS agent.text / agent.tool_call / agent.tool_result events back to
#        /internal/v1/runs/{id}/events (server-allocated seq)
#     -> runner uploads the diff to /internal/v1/runs/{id}/artifact
#     -> run transitions to succeeded
#
# Assertions (all against the live orchestrator REST/SSE API):
#   - run reaches status=succeeded
#   - the event log contains agent.text AND a tool_call AND a tool_result AND a
#     run.artifact AND a terminal run.status(succeeded), with a UNIQUE, GAPLESS
#     seq sequence (proves the seq-collision fix)
#   - the artifact endpoint returns the scripted diff
#
# Requirements: docker, go, and the dev Postgres (compose.yml). Everything else
# (mockllm, orchestrator, runner) is built and started by this script.
#
# Env: KEEP=1 to leave containers/tmp for inspection; ORCH_PORT to override.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ORCH_DIR="$(cd "$HERE/../orchestrator" && pwd)"
IMAGE="${IMAGE:-jcode-runner:local}"
NET="jcode-itest-net"
MOCK_CTR="jcode-itest-mockllm"
ORCH_PORT="${ORCH_PORT:-8090}"
CONSOLE_TOKEN="itest-console-token"
PG_DSN="${PG_DSN:-postgres://jcloud:jcloud@localhost:5432/jcloud?sslmode=disable}"
SCENARIO="write_file"
EXPECT_FILE="HELLO_FROM_JCODE.txt"
EXPECT_STR="jcode ran headless in a container"

pass() { printf '\033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$*"; dump; cleanup; exit 1; }
info() { printf '[itest] %s\n' "$*"; }

ORCH_PID=""
dump() {
  [ -n "${TMP:-}" ] || return 0
  echo "----- orchestrator log (tail) -----"; tail -40 "$TMP/orch.log" 2>/dev/null || true
  echo "----- mockllm log (tail) -----"; docker logs "$MOCK_CTR" 2>&1 | tail -20 || true
  echo "----- runner containers -----"; docker ps -a --filter "label=jcloud.run-id" --format '{{.Names}} {{.Status}}' || true
}

cleanup() {
  [ "${KEEP:-0}" = "1" ] && { info "KEEP=1, leaving state"; return; }
  [ -n "$ORCH_PID" ] && kill "$ORCH_PID" 2>/dev/null || true
  # Remove any runner containers this run created.
  docker ps -aq --filter "label=jcloud.run-id" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker rm -f "$MOCK_CTR" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  [ -n "${TMP:-}" ] && rm -rf "$TMP" 2>/dev/null || true
}
trap cleanup EXIT

# --- arch ---
case "$(uname -m)" in
  arm64|aarch64) TARGETARCH=arm64 ;;
  x86_64|amd64)  TARGETARCH=amd64 ;;
  *) fail "unsupported host arch $(uname -m)" ;;
esac
export TARGETARCH

# jq is used to parse REST/SSE JSON. Fall back to a python one-liner if absent.
if command -v jq >/dev/null 2>&1; then
  JQ() { jq "$@"; }
else
  JQ() { fail "jq is required for test-integration.sh"; }
fi

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }
need docker; need go; need curl; need jq

# === 0. Postgres reachable? ===
info "checking Postgres at $PG_DSN"
if ! (exec 3<>/dev/tcp/localhost/5432) 2>/dev/null; then
  fail "Postgres not reachable on :5432 — run 'make -C ../orchestrator pg-up' first"
fi
exec 3>&- 3<&- 2>/dev/null || true
pass "Postgres reachable"

# === 1. build runner image (jcode + acpdrive + orchclient + mockllm) ===
info "building runner image + binaries (linux/$TARGETARCH)"
IMAGE="$IMAGE" TARGETARCH="$TARGETARCH" bash "$HERE/build.sh" >/dev/null || fail "build.sh failed"
pass "runner image built: $IMAGE"

# === 2. build orchestrator binary ===
TMP="$(mktemp -d)"
info "building orchestrator binary"
( cd "$ORCH_DIR" && go build -o "$TMP/orchestrator" ./cmd/orchestrator ) || fail "orchestrator build failed"
pass "orchestrator built"

# === 3. seed repo (mounted into the runner container via a volume) ===
SEED="$TMP/seed"
mkdir -p "$SEED"
(
  cd "$SEED"
  git init -q -b main
  git config user.email seed@jcode.local
  git config user.name  seed
  printf '# Seed repo\n\nCloned by the runner via the process launcher.\n' > README.md
  git add -A
  git commit -qm "initial seed commit"
)
info "seed repo at $SEED (commit $(git -C "$SEED" rev-parse --short HEAD))"

# === 4. docker network + mockllm sidecar ===
docker network create "$NET" >/dev/null 2>&1 || true
docker rm -f "$MOCK_CTR" >/dev/null 2>&1 || true
info "starting mockllm sidecar on network $NET"
docker run -d --name "$MOCK_CTR" --network "$NET" --network-alias mockllm \
  --platform "linux/$TARGETARCH" \
  -e MOCK_ADDR=":8081" -e MOCK_SCENARIO="$SCENARIO" \
  --entrypoint /usr/local/bin/mockllm "$IMAGE" >/dev/null || fail "mockllm sidecar failed to start"
# wait for mockllm
ready=0
for _ in $(seq 1 50); do
  if docker run --rm --network "$NET" --platform "linux/$TARGETARCH" \
       --entrypoint bash "$IMAGE" -c '(exec 3<>/dev/tcp/mockllm/8081) 2>/dev/null' >/dev/null 2>&1; then
    ready=1; break
  fi
  sleep 0.2
done
[ "$ready" = "1" ] || { docker logs "$MOCK_CTR" || true; fail "mockllm never became ready"; }
pass "mockllm sidecar up"

# === 5. start the orchestrator (process launcher) ===
# The runner container must reach:
#   - the orchestrator on the HOST  => host.docker.internal:$ORCH_PORT
#   - mockllm on the docker network => mockllm:8081 (network alias)
#   - the seed repo                 => /seed (bind mount) cloned via file:///seed
# RUNNER_DOCKER_ARGS injects the seed mount + host-gateway alias into `docker run`.
info "starting orchestrator on :$ORCH_PORT (JOB_LAUNCHER=process)"
(
  cd "$ORCH_DIR"
  ADDR=":$ORCH_PORT" \
  CONSOLE_TOKEN="$CONSOLE_TOKEN" \
  DATABASE_URL="$PG_DSN" \
  JOB_LAUNCHER=process \
  RUNNER_IMAGE="$IMAGE" \
  RUNNER_NETWORK="$NET" \
  RUNNER_DOCKER_ARGS="--platform linux/$TARGETARCH --add-host=host.docker.internal:host-gateway -v $SEED:/seed:ro" \
  ORCH_BASE_URL="http://host.docker.internal:$ORCH_PORT" \
  MODEL_BASE_URL="http://mockllm:8081/v1" \
  MODEL_API_KEY="dummy-key" \
  MODEL_NAME="mock/mock-model" \
  RECONCILE_INTERVAL=1s \
  MAX_CONCURRENT_RUNS=4 \
  "$TMP/orchestrator" >"$TMP/orch.log" 2>&1 &
  echo $! > "$TMP/orch.pid"
)
ORCH_PID="$(cat "$TMP/orch.pid")"

# wait for /healthz
for _ in $(seq 1 50); do
  if curl -fsS "http://localhost:$ORCH_PORT/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.2
done
curl -fsS "http://localhost:$ORCH_PORT/healthz" >/dev/null 2>&1 || fail "orchestrator did not become healthy"
pass "orchestrator healthy on :$ORCH_PORT"

API="http://localhost:$ORCH_PORT/api/v1"
AUTH=(-H "Authorization: Bearer $CONSOLE_TOKEN")

# === 6. create project + run via REST ===
info "creating project (repo file:///seed)"
PROJ_JSON="$(curl -fsS "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d '{"name":"itest","repo_url":"file:///seed","default_branch":"main"}' \
  "$API/projects")" || fail "create project failed"
PROJ_ID="$(printf '%s' "$PROJ_JSON" | JQ -r .id)"
[ -n "$PROJ_ID" ] && [ "$PROJ_ID" != null ] || fail "no project id"
pass "project created: $PROJ_ID"

info "creating run"
RUN_JSON="$(curl -fsS "${AUTH[@]}" -H 'Content-Type: application/json' \
  -d "{\"prompt\":\"Create a file $EXPECT_FILE in the repository root.\"}" \
  "$API/projects/$PROJ_ID/runs")" || fail "create run failed"
RUN_ID="$(printf '%s' "$RUN_JSON" | JQ -r .id)"
[ -n "$RUN_ID" ] && [ "$RUN_ID" != null ] || fail "no run id"
pass "run created: $RUN_ID (status=$(printf '%s' "$RUN_JSON" | JQ -r .status))"

# === 7. wait for a terminal status ===
info "waiting for run to finish (up to 120s)"
STATUS=""
for _ in $(seq 1 240); do
  STATUS="$(curl -fsS "${AUTH[@]}" "$API/runs/$RUN_ID" | JQ -r .status)"
  case "$STATUS" in
    succeeded|failed|canceled) break ;;
  esac
  sleep 0.5
done
info "final status: $STATUS"
[ "$STATUS" = "succeeded" ] || fail "run status=$STATUS want succeeded"
pass "run reached succeeded"

# === 8. assert the event log ===
EVENTS_JSON="$(curl -fsS "${AUTH[@]}" "$API/runs/$RUN_ID/events?limit=1000")"
echo "----- events -----"; printf '%s' "$EVENTS_JSON" | JQ -c '.events[] | {seq,type}'; echo "------------------"

have_type() { printf '%s' "$EVENTS_JSON" | JQ -e --arg t "$1" '.events | map(.type) | index($t) != null' >/dev/null; }
have_type agent.text        || fail "no agent.text event streamed from the runner"
have_type agent.tool_call   || fail "no agent.tool_call event streamed from the runner"
have_type agent.tool_result || fail "no agent.tool_result event streamed from the runner"
have_type run.artifact      || fail "no run.artifact event (artifact upload signal)"
pass "runner streamed agent.text + tool_call + tool_result + run.artifact"

# terminal run.status(succeeded) present
printf '%s' "$EVENTS_JSON" | JQ -e '.events | map(select(.type=="run.status" and .payload.status=="succeeded")) | length > 0' >/dev/null \
  || fail "no terminal run.status(succeeded) event"
pass "terminal run.status(succeeded) present"

# seq must be unique, gapless, monotonic from 1 (proves the seq-collision fix:
# runner events + internal events coexist without drops).
SEQ_OK="$(printf '%s' "$EVENTS_JSON" | JQ '[.events[].seq] as $s | ($s | length) as $n | ($s == ([range(1; $n+1)]))')"
[ "$SEQ_OK" = "true" ] || fail "event seqs are not unique/gapless/monotonic (collision?): $(printf '%s' "$EVENTS_JSON" | JQ -c '[.events[].seq]')"
pass "event seqs are unique, gapless, monotonic (no collision / no drops)"

# === 9. assert the artifact ===
ART_JSON="$(curl -fsS "${AUTH[@]}" "$API/runs/$RUN_ID/artifact")" || fail "artifact fetch failed"
ART_CONTENT="$(printf '%s' "$ART_JSON" | JQ -r .content)"
printf '%s' "$ART_CONTENT" | grep -q "$EXPECT_FILE" || fail "artifact diff does not mention $EXPECT_FILE"
printf '%s' "$ART_CONTENT" | grep -q "$EXPECT_STR"  || fail "artifact diff missing scripted content"
pass "artifact diff present and contains the scripted change"

echo
printf '\033[32m===================================================\033[0m\n'
printf '\033[32m  PROVEN (no k8s): REST -> runner (process launcher) \033[0m\n'
printf '\033[32m  -> live events streamed -> diff artifact uploaded  \033[0m\n'
printf '\033[32m  -> run succeeded. seq unique+gapless (no collision).\033[0m\n'
printf '\033[32m===================================================\033[0m\n'
