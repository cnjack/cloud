#!/usr/bin/env bash
# test-bundle.sh — proof of the M3 runner contract's WRITE path: in draft_pr mode
# the runner produces a git BUNDLE and POSTs it to the orchestrator, and it NEVER
# pushes (the orchestrator pushes + opens the PR on the user's behalf). It also
# re-proves the readonly default stays diff-only (no bundle).
#
# This replaces the old test-gitea-push.sh, which proved the (now removed) runner
# self-push. There is no Gitea and no provider token in the pod at all: a tiny
# Python mock orchestrator captures the POSTed bundle over the RUN_TOKEN.
#
# Flow:
#   1. build the runner image (build.sh)
#   2. start a Python mock orchestrator on the host that accepts
#      POST /internal/v1/runs/<id>/bundle (saves the body) and 200s the
#      events/artifact endpoints
#   3. run the runner (GIT_MODE=draft_pr, SOURCE_MODE=clone against a local seed,
#      START_MOCKLLM=1) pointed at the mock via host.docker.internal
#   4. assert: exit 0; the orchestrator received a VALID git bundle whose ref is
#      the jcode/run-<id> branch; the runner logs show NO `git push`
#   5. readonly re-run: assert NO bundle is POSTed
#
# Env: IMAGE (default jcode-runner:local), TARGETARCH (host arch), KEEP=1.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${IMAGE:-jcode-runner:local}"
RUN_CTR="jcode-bundle-test"
MOCK_PORT="${MOCK_PORT:-8091}"

pass() { printf '\033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$*"; dump; cleanup; exit 1; }
info() { printf '[bundle-test] %s\n' "$*"; }

MOCK_PID=""
dump() {
  echo "----- runner output (tail) -----"; tail -40 "${TMP:-/dev/null}/run.out" 2>/dev/null || true
}
cleanup() {
  [ "${KEEP:-0}" = "1" ] && { info "KEEP=1, leaving state"; return; }
  docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
  [ -n "$MOCK_PID" ] && kill "$MOCK_PID" 2>/dev/null || true
  [ -n "${TMP:-}" ] && rm -rf "$TMP" 2>/dev/null || true
}
trap cleanup EXIT

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }
need docker; need git; need python3

if [ -z "${TARGETARCH:-}" ]; then
  case "$(uname -m)" in
    arm64|aarch64) TARGETARCH=arm64 ;;
    x86_64|amd64)  TARGETARCH=amd64 ;;
    *) fail "unsupported host arch $(uname -m)" ;;
  esac
fi
export TARGETARCH

# === 1. build ===
info "building runner image (linux/$TARGETARCH)"
IMAGE="$IMAGE" TARGETARCH="$TARGETARCH" bash "$HERE/build.sh" >/dev/null || fail "build.sh failed"

# === 2. seed repo + mock orchestrator ===
TMP="$(mktemp -d)"
SEED="$TMP/seed"; OUT="$TMP/orch"; mkdir -p "$SEED" "$OUT"
(
  cd "$SEED"; git init -q -b main
  git config user.email seed@jcode.local; git config user.name seed
  printf '# seed\n' > README.md; git add -A; git commit -qm init
)

cat > "$TMP/mock.py" <<'PY'
import http.server, sys, os
OUT = sys.argv[1]
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get('Content-Length', '0')); body = self.rfile.read(n)
        if self.path.endswith('/bundle'):
            open(os.path.join(OUT, 'received.bundle'), 'wb').write(body)
            self.send_response(201); self.end_headers(); self.wfile.write(b'{"kind":"bundle"}')
        elif self.path.endswith('/review'):
            open(os.path.join(OUT, 'received.review'), 'wb').write(body)
            self.send_response(201); self.end_headers(); self.wfile.write(b'{"kind":"review"}')
        else:
            self.send_response(200); self.end_headers(); self.wfile.write(b'{}')
    def log_message(self, *a): pass
http.server.HTTPServer(('0.0.0.0', int(sys.argv[2])), H).serve_forever()
PY
python3 "$TMP/mock.py" "$OUT" "$MOCK_PORT" &
MOCK_PID=$!
sleep 0.5
info "mock orchestrator on :$MOCK_PORT (captures bundles to $OUT)"

# === 3. run the runner in DRAFT_PR (bundle) mode ===
RUN_ID="bundle-$RANDOM"
BR="jcode/run-${RUN_ID:0:8}"
info "running runner draft_pr run_id=$RUN_ID (SOURCE_MODE=clone local seed)"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --platform "linux/$TARGETARCH" \
  --add-host=host.docker.internal:host-gateway \
  -v "$SEED:/seed:ro" \
  -e RUN_ID="$RUN_ID" \
  -e TASK_PROMPT="Create a file HELLO_FROM_JCODE.txt in the repository root." \
  -e SOURCE_MODE="clone" \
  -e REPO_URL="file:///seed" \
  -e BASE_BRANCH="main" \
  -e GIT_MODE="draft_pr" \
  -e BRANCH_NAME="$BR" \
  -e START_MOCKLLM=1 -e MOCK_SCENARIO="write_file" \
  -e MODEL_NAME="mock/mock-model" -e MODEL_API_KEY="dummy-key" \
  -e ORCH_BASE_URL="http://host.docker.internal:$MOCK_PORT" \
  -e RUN_TOKEN="run-token-xyz" \
  "$IMAGE" >"$TMP/run.out" 2>&1
RC=$?
set -e
cat "$TMP/run.out"
[ "$RC" -eq 0 ] || fail "runner (draft_pr) exited $RC (want 0)"
pass "runner (draft_pr) exited 0"

# === 4. the orchestrator received a valid bundle; the runner did NOT push ===
[ -s "$OUT/received.bundle" ] || fail "orchestrator did not receive a bundle"
git bundle verify "$OUT/received.bundle" >/dev/null 2>&1 || fail "received bundle is not a valid git bundle"
if ! git bundle list-heads "$OUT/received.bundle" | grep -q "refs/heads/$BR"; then
  echo "----- bundle heads -----"; git bundle list-heads "$OUT/received.bundle"
  fail "bundle does not carry branch $BR"
fi
pass "orchestrator received a VALID bundle carrying $BR"

# The runner must NOT push: no `git push` anywhere in its output, and no
# GIT_TOKEN/GIT_PUSH_URL contract remains.
if grep -qiE 'git push|pushing|GIT_PUSH_URL' "$TMP/run.out"; then
  echo "----- runner output -----"; cat "$TMP/run.out"
  fail "runner attempted to push (M3: the runner must NEVER push)"
fi
pass "runner produced a bundle and did NOT push (control plane owns the push)"

# === 5. readonly re-run posts NO bundle ===
rm -f "$OUT/received.bundle"
RO_ID="bundle-ro-$RANDOM"
info "running runner readonly run_id=$RO_ID (must NOT bundle)"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --platform "linux/$TARGETARCH" \
  --add-host=host.docker.internal:host-gateway \
  -v "$SEED:/seed:ro" \
  -e RUN_ID="$RO_ID" \
  -e TASK_PROMPT="Create a file HELLO_FROM_JCODE.txt in the repository root." \
  -e SOURCE_MODE="clone" -e REPO_URL="file:///seed" -e BASE_BRANCH="main" \
  -e START_MOCKLLM=1 -e MOCK_SCENARIO="write_file" \
  -e MODEL_NAME="mock/mock-model" -e MODEL_API_KEY="dummy-key" \
  -e ORCH_BASE_URL="http://host.docker.internal:$MOCK_PORT" \
  -e RUN_TOKEN="run-token-ro" \
  "$IMAGE" >"$TMP/ro.out" 2>&1
RO_RC=$?
set -e
[ "$RO_RC" -eq 0 ] || { cat "$TMP/ro.out"; fail "readonly run exited $RO_RC (want 0)"; }
[ ! -s "$OUT/received.bundle" ] || fail "readonly run POSTed a bundle (default must be diff-only!)"
pass "readonly (default) run succeeded and produced NO bundle"

# === 6. update mode (M7): BRANCH_NAME == BASE_BRANCH pushes back onto the PR ===
# A webhook @mention task builds ON the PR head branch: the orchestrator sets
# BRANCH_NAME == BASE_BRANCH, and the runner must commit onto that SAME branch
# (not `checkout -b` a new one) and bundle <start SHA>..HEAD carrying it.
rm -f "$OUT/received.bundle"
UP_ID="bundle-up-$RANDOM"
info "running runner draft_pr UPDATE mode run_id=$UP_ID (BRANCH_NAME==BASE_BRANCH==main)"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --platform "linux/$TARGETARCH" \
  --add-host=host.docker.internal:host-gateway \
  -v "$SEED:/seed:ro" \
  -e RUN_ID="$UP_ID" \
  -e TASK_PROMPT="Create a file CONTRIBUTING.md in the repository root." \
  -e SOURCE_MODE="clone" -e REPO_URL="file:///seed" -e BASE_BRANCH="main" \
  -e GIT_MODE="draft_pr" -e BRANCH_NAME="main" \
  -e START_MOCKLLM=1 -e MOCK_SCENARIO="write_file" \
  -e MODEL_NAME="mock/mock-model" -e MODEL_API_KEY="dummy-key" \
  -e ORCH_BASE_URL="http://host.docker.internal:$MOCK_PORT" \
  -e RUN_TOKEN="run-token-up" \
  "$IMAGE" >"$TMP/up.out" 2>&1
UP_RC=$?
set -e
cat "$TMP/up.out"
[ "$UP_RC" -eq 0 ] || fail "update-mode run exited $UP_RC (want 0)"
[ -s "$OUT/received.bundle" ] || fail "update-mode run did not POST a bundle"
git bundle verify "$OUT/received.bundle" >/dev/null 2>&1 || fail "update-mode bundle is not valid"
if ! git bundle list-heads "$OUT/received.bundle" | grep -q "refs/heads/main"; then
  echo "----- bundle heads -----"; git bundle list-heads "$OUT/received.bundle"
  fail "update-mode bundle does not carry refs/heads/main (BRANCH_NAME==BASE_BRANCH)"
fi
if grep -qiE 'git push|pushing|GIT_PUSH_URL' "$TMP/up.out"; then
  fail "update-mode run attempted to push (the runner must NEVER push)"
fi
pass "update-mode run committed onto BASE_BRANCH and bundled refs/heads/main (no push)"

echo
printf '\033[32m===========================================================\033[0m\n'
printf '\033[32m  PROVEN (M3): draft_pr runner POSTs a valid bundle to the  \033[0m\n'
printf '\033[32m  orchestrator and NEVER pushes; readonly stays diff-only.  \033[0m\n'
printf '\033[32m===========================================================\033[0m\n'
