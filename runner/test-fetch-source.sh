#!/usr/bin/env bash
# test-fetch-source.sh — proof of the M3 runner contract's READ path and the
# REVIEW channel. Replaces the old test-private-clone.sh (the runner no longer
# clones a private repo with a token — a private/provider repo is read via a
# source BUNDLE the orchestrator serves over the RUN_TOKEN, so NO credential ever
# enters the pod).
#
# Flow:
#   1. build the runner image (build.sh)
#   2. build a source bundle (git bundle create --all) from a seed repo that has
#      a `main` branch and a `feature` branch — served by a Python mock
#      orchestrator at GET /internal/v1/runs/<id>/source
#   3. AGENT/fetch: run the runner with SOURCE_MODE=fetch (no REPO_URL). Assert it
#      downloads the bundle, clones it, runs, and produces a diff — no token used.
#   4. REVIEW: run the runner with RUN_KIND=review, PR_HEAD=feature, PR_BASE=main.
#      The mockllm auto-detects the "[review]" prompt and writes REVIEW.md; assert
#      the orchestrator receives the review markdown.
#
# Env: IMAGE (default jcode-runner:local), TARGETARCH (host arch), KEEP=1.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${IMAGE:-jcode-runner:local}"
RUN_CTR="jcode-fetch-test"
MOCK_PORT="${MOCK_PORT:-8092}"

pass() { printf '\033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$*"; dump; cleanup; exit 1; }
info() { printf '[fetch-test] %s\n' "$*"; }

MOCK_PID=""
dump() { echo "----- runner output (tail) -----"; tail -40 "${TMP:-/dev/null}/run.out" 2>/dev/null || true; }
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

info "building runner image (linux/$TARGETARCH)"
IMAGE="$IMAGE" TARGETARCH="$TARGETARCH" bash "$HERE/build.sh" >/dev/null || fail "build.sh failed"

# === seed repo with main + feature, and an all-refs source bundle ===
TMP="$(mktemp -d)"
SEED="$TMP/seed"; OUT="$TMP/orch"; mkdir -p "$SEED" "$OUT"
(
  cd "$SEED"; git init -q -b main
  git config user.email seed@jcode.local; git config user.name seed
  printf '# seed\n' > README.md; git add -A; git commit -qm init
  git checkout -q -b feature
  printf 'new feature line\n' > FEATURE.txt; git add -A; git commit -qm feature
  git checkout -q main
)
SRC_BUNDLE="$OUT/source.bundle"
git -C "$SEED" bundle create "$SRC_BUNDLE" --all >/dev/null 2>&1 || fail "could not build source bundle"
git bundle verify "$SRC_BUNDLE" >/dev/null 2>&1 || fail "source bundle invalid"

cat > "$TMP/mock.py" <<'PY'
import http.server, sys, os
OUT = sys.argv[1]; SRC = sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.endswith('/source'):
            data = open(SRC, 'rb').read()
            self.send_response(200); self.send_header('Content-Type', 'application/octet-stream')
            self.send_header('Content-Length', str(len(data))); self.end_headers(); self.wfile.write(data)
        else:
            self.send_response(404); self.end_headers()
    def do_POST(self):
        n = int(self.headers.get('Content-Length', '0')); body = self.rfile.read(n)
        if self.path.endswith('/review'):
            open(os.path.join(OUT, 'received.review'), 'wb').write(body)
            self.send_response(201); self.end_headers(); self.wfile.write(b'{"kind":"review"}')
        elif self.path.endswith('/bundle'):
            open(os.path.join(OUT, 'received.bundle'), 'wb').write(body)
            self.send_response(201); self.end_headers(); self.wfile.write(b'{}')
        else:
            self.send_response(200); self.end_headers(); self.wfile.write(b'{}')
    def log_message(self, *a): pass
http.server.HTTPServer(('0.0.0.0', int(sys.argv[3])), H).serve_forever()
PY
python3 "$TMP/mock.py" "$OUT" "$SRC_BUNDLE" "$MOCK_PORT" &
MOCK_PID=$!
sleep 0.5
info "mock orchestrator on :$MOCK_PORT (serves source bundle; captures reviews)"

# === 3. AGENT run over SOURCE_MODE=fetch (no REPO_URL, no token) ===
A_ID="fetch-$RANDOM"
info "[A] runner agent SOURCE_MODE=fetch run_id=$A_ID"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --platform "linux/$TARGETARCH" \
  --add-host=host.docker.internal:host-gateway \
  -e RUN_ID="$A_ID" \
  -e TASK_PROMPT="Create a file HELLO_FROM_JCODE.txt in the repository root." \
  -e SOURCE_MODE="fetch" \
  -e BASE_BRANCH="main" \
  -e GIT_MODE="readonly" \
  -e START_MOCKLLM=1 -e MOCK_SCENARIO="write_file" \
  -e MODEL_NAME="mock/mock-model" -e MODEL_API_KEY="dummy-key" \
  -e ORCH_BASE_URL="http://host.docker.internal:$MOCK_PORT" \
  -e RUN_TOKEN="run-token-fetch" \
  "$IMAGE" >"$TMP/run.out" 2>&1
A_RC=$?
set -e
cat "$TMP/run.out"
[ "$A_RC" -eq 0 ] || fail "[A] fetch-mode agent run exited $A_RC (want 0)"
grep -q "fetching source bundle" "$TMP/run.out" || fail "[A] runner did not fetch the source bundle"
grep -q "===JCODE_DIFF_BEGIN" "$TMP/run.out" || fail "[A] runner produced no diff"
pass "[A] runner fetched the source bundle (no token) and produced a diff"

# === 4. REVIEW run: PR_HEAD=feature PR_BASE=main → REVIEW.md posted ===
R_ID="review-$RANDOM"
info "[B] runner review run_id=$R_ID (PR_BASE=main PR_HEAD=feature)"
docker rm -f "$RUN_CTR" >/dev/null 2>&1 || true
set +e
docker run --name "$RUN_CTR" --platform "linux/$TARGETARCH" \
  --add-host=host.docker.internal:host-gateway \
  -e RUN_ID="$R_ID" \
  -e TASK_PROMPT="placeholder (a review prompt is built internally)" \
  -e RUN_KIND="review" \
  -e SOURCE_MODE="fetch" \
  -e BASE_BRANCH="main" \
  -e PR_BASE="main" -e PR_HEAD="feature" \
  -e START_MOCKLLM=1 \
  -e MODEL_NAME="mock/mock-model" -e MODEL_API_KEY="dummy-key" \
  -e ORCH_BASE_URL="http://host.docker.internal:$MOCK_PORT" \
  -e RUN_TOKEN="run-token-review" \
  "$IMAGE" >"$TMP/review.out" 2>&1
R_RC=$?
set -e
cat "$TMP/review.out"
[ "$R_RC" -eq 0 ] || fail "[B] review run exited $R_RC (want 0)"
[ -s "$OUT/received.review" ] || fail "[B] orchestrator did not receive a review"
if ! grep -qiE 'approve|needs-work' "$OUT/received.review"; then
  echo "----- received review -----"; cat "$OUT/received.review"
  fail "[B] review output has no conclusion (approve|needs-work)"
fi
# A review run must NOT produce a bundle.
[ ! -s "$OUT/received.bundle" ] || fail "[B] review run POSTed a bundle (must skip diff/bundle)"
pass "[B] review run posted REVIEW.md with a conclusion and produced no bundle"

echo
printf '\033[32m===========================================================\033[0m\n'
printf '\033[32m  PROVEN (M3): the runner READS a provider repo via a source \033[0m\n'
printf '\033[32m  bundle (no token in the pod) and the review channel posts  \033[0m\n'
printf '\033[32m  REVIEW.md to the orchestrator.                              \033[0m\n'
printf '\033[32m===========================================================\033[0m\n'
