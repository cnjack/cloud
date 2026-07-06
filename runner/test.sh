#!/usr/bin/env bash
# test.sh — full local proof that jcode runs FULLY HEADLESS (no TTY) in a Linux
# container, executes a coding task against a cloned repo, and produces a diff.
#
# Flow:
#   1. build.sh: cross-compile jcode(headless)+acpdrive+mockllm and build the image
#   2. create a throwaway git repo as the "REPO_URL" (mounted read-only, cloned
#      inside the container via file://)
#   3. start mockllm as a SIDECAR container on a private docker network
#   4. run the runner container with NO TTY (no -t) against mockllm:8081
#   5. assert: exit 0, /out/diff.patch non-empty + contains the scripted change,
#      no TUI escape codes in the logs, both agent turns hit the mock
#
# Env: IMAGE (default jcode-runner:local), TARGETARCH (default host arch),
#      KEEP=1 to keep the temp dir + containers for inspection.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${IMAGE:-jcode-runner:local}"
NET="jcode-runner-test-net"
MOCK_CTR="jcode-mockllm-test"
RUN_CTR="jcode-runner-test"
SCENARIO="write_file"
EXPECT_FILE="HELLO_FROM_JCODE.txt"
EXPECT_STR="jcode ran headless in a container"

pass() { printf '\033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$*"; cleanup; exit 1; }
info() { printf '[test] %s\n' "$*"; }

cleanup() {
  [ "${KEEP:-0}" = "1" ] && { info "KEEP=1, leaving containers/net/tmp"; return; }
  docker rm -f "$MOCK_CTR" "$RUN_CTR" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  [ -n "${TMP:-}" ] && rm -rf "$TMP" 2>/dev/null || true
}
trap cleanup EXIT

# --- arch ---
if [ -z "${TARGETARCH:-}" ]; then
  case "$(uname -m)" in
    arm64|aarch64) TARGETARCH=arm64 ;;
    x86_64|amd64)  TARGETARCH=amd64 ;;
    *) fail "unsupported host arch $(uname -m)" ;;
  esac
fi
export TARGETARCH

# === 1. build ===
info "building binaries + image (linux/$TARGETARCH)"
IMAGE="$IMAGE" TARGETARCH="$TARGETARCH" bash "$HERE/build.sh" || fail "build.sh failed"

# === 2. throwaway seed repo ===
TMP="$(mktemp -d)"
SEED="$TMP/seed"
OUT="$TMP/out"
mkdir -p "$SEED" "$OUT"
(
  cd "$SEED"
  git init -q -b main
  git config user.email seed@jcode.local
  git config user.name  seed
  printf '# Seed repo\n\nA throwaway repo cloned by the runner.\n' > README.md
  git add -A
  git commit -qm "initial seed commit"
)
info "seed repo at $SEED (commit $(git -C "$SEED" rev-parse --short HEAD))"

# === 3. docker network + mockllm sidecar ===
docker network create "$NET" >/dev/null 2>&1 || true
info "starting mockllm sidecar ($MOCK_CTR) on network $NET"
docker rm -f "$MOCK_CTR" >/dev/null 2>&1 || true
docker run -d --name "$MOCK_CTR" --network "$NET" --network-alias mockllm \
  --platform "linux/$TARGETARCH" \
  -e MOCK_ADDR=":8081" -e MOCK_SCENARIO="$SCENARIO" \
  --entrypoint /usr/local/bin/mockllm \
  "$IMAGE" >/dev/null || fail "failed to start mockllm sidecar"

# wait for the sidecar to be reachable from a throwaway container on the net
info "waiting for mockllm to accept connections"
ready=0
for _ in $(seq 1 50); do
  if docker run --rm --network "$NET" --platform "linux/$TARGETARCH" \
       --entrypoint bash "$IMAGE" -c '(exec 3<>/dev/tcp/mockllm/8081) 2>/dev/null' >/dev/null 2>&1; then
    ready=1; break
  fi
  sleep 0.2
done
[ "$ready" = "1" ] || { docker logs "$MOCK_CTR" || true; fail "mockllm never became ready"; }
pass "mockllm sidecar is up"

# === 4. run the runner container — NO TTY (no -t!) ===
info "running runner container (NO TTY) against mockllm:8081"
set +e
docker run --name "$RUN_CTR" --network "$NET" --platform "linux/$TARGETARCH" \
  -v "$SEED:/seed:ro" \
  -v "$OUT:/out" \
  -e REPO_URL="file:///seed" \
  -e TASK_PROMPT="Create a file $EXPECT_FILE in the repository root." \
  -e MODEL_BASE_URL="http://mockllm:8081/v1" \
  -e MODEL_API_KEY="dummy-key" \
  -e MODEL_NAME="mock/mock-model" \
  -e RUN_ID="proof-1" \
  "$IMAGE" > "$TMP/run.stdout" 2> "$TMP/run.stderr"
RC=$?
set -e

echo "----- runner stdout -----"; cat "$TMP/run.stdout"
echo "----- runner stderr (tail) -----"; tail -30 "$TMP/run.stderr"
echo "-------------------------"

# === 5. assertions ===
[ "$RC" -eq 0 ] || fail "runner exited non-zero (rc=$RC)"
pass "runner exited 0"

[ -s "$OUT/diff.patch" ] || fail "/out/diff.patch is empty or missing"
pass "diff.patch is non-empty ($(wc -c < "$OUT/diff.patch" | tr -d ' ') bytes)"

grep -q "$EXPECT_FILE" "$OUT/diff.patch" || fail "diff.patch does not mention $EXPECT_FILE"
grep -q "$EXPECT_STR"  "$OUT/diff.patch" || fail "diff.patch missing scripted content"
grep -q '^new file mode' "$OUT/diff.patch" || fail "diff.patch is not a valid new-file diff"
pass "diff.patch contains the mock-scripted change to $EXPECT_FILE"

# stdout must carry the diff between markers too
grep -q '===JCODE_DIFF_BEGIN' "$TMP/run.stdout" || fail "stdout missing diff markers"
pass "stdout carries the diff between markers"

# no TUI escape sequences anywhere (bracketed-paste / alt-screen / cursor moves)
if LC_ALL=C grep -aP '\x1b\[\?(1049|2004|25)[hl]|\x1b\[[0-9;]*[ABCDHJK]' \
     "$TMP/run.stdout" "$TMP/run.stderr" >/dev/null 2>&1; then
  fail "found TUI/ANSI escape sequences in output (TUI leaked?)"
fi
pass "no TUI escape codes in logs"

# both agent turns must have reached the mock (turn1 tool call + turn2 finish)
MOCKLOG="$(docker logs "$MOCK_CTR" 2>&1 || true)"
echo "----- mockllm log -----"; echo "$MOCKLOG"; echo "-----------------------"
echo "$MOCKLOG" | grep -q 'turn2=false' || fail "mock never saw turn 1 (tool call)"
echo "$MOCKLOG" | grep -q 'turn2=true'  || fail "mock never saw turn 2 (final message)"
pass "both agent turns reached the mock (full loop)"

echo
printf '\033[32m========================================\033[0m\n'
printf '\033[32m  PROVEN: jcode ran headless (no TTY),  \033[0m\n'
printf '\033[32m  cloned a repo, executed a task, and   \033[0m\n'
printf '\033[32m  produced a valid diff.                \033[0m\n'
printf '\033[32m========================================\033[0m\n'
