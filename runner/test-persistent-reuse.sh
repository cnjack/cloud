#!/usr/bin/env bash
# test-persistent-reuse.sh — proves Feature C's persistent-workspace REUSE path in
# entrypoint.sh: with PERSISTENT_WORKSPACE=1, a second run against an existing
# checkout FETCHES + resets in place instead of re-cloning, keeps the jcode memory
# HOME, and cleans up the prior run's pollution.
#
# It runs entrypoint.sh DIRECTLY (bash, no docker): in SOURCE_MODE=clone with the
# test-only JCLOUD_PREP_ONLY=1 hook, the script stops right after workspace
# preparation + config write — so it needs only `git` and `bash`, no jcode/model.
# That keeps the test cheap while exercising the REAL prep code (not a copy).
#
# Assertions:
#   1. run 1 (empty PVC)      -> fresh clone, HEAD == commit A
#   2. run 2 (existing .git)  -> REUSE: HEAD advances to commit B (fetched latest),
#                                the .git marker survives (NOT re-cloned), the
#                                untracked pollution file is gone (clean baseline),
#                                and $HOME/.jcode memory is preserved.
#   3. config.json has "memory": { "enabled": true } in persistent mode.
#
# Env: KEEP=1 keeps the scratch dir.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENTRY="$HERE/entrypoint.sh"

pass() { printf '\033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$*"; exit 1; }
info() { printf '[reuse-test] %s\n' "$*"; }

command -v git >/dev/null 2>&1 || fail "git is required"

TMP="$(mktemp -d)"
cleanup() { [ "${KEEP:-0}" = "1" ] && { info "KEEP=1, leaving $TMP"; return; }; rm -rf "$TMP"; }
trap cleanup EXIT

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

# --- an "origin" repo with commit A on main ---------------------------------
ORIGIN="$TMP/origin.git"
git init -q --bare "$ORIGIN"
SEED="$TMP/seed"
git init -q "$SEED"; git -C "$SEED" checkout -q -B main
echo "v1" > "$SEED/file.txt"
git -C "$SEED" add -A && git -C "$SEED" commit -q -m "A"
git -C "$SEED" remote add origin "$ORIGIN" && git -C "$SEED" push -q origin main
COMMIT_A="$(git -C "$SEED" rev-parse HEAD)"

WS="$TMP/ws"
HOME_DIR="$TMP/home"
mkdir -p "$HOME_DIR"

run_prep() {
  env -i PATH="$PATH" HOME="$HOME_DIR" \
    WORKSPACE="$WS" \
    PERSISTENT_WORKSPACE=1 \
    SOURCE_MODE=clone \
    REPO_URL="$ORIGIN" \
    BASE_BRANCH=main \
    RUN_KIND=agent \
    TASK_PROMPT="do it" \
    MODEL_BASE_URL="http://model.invalid/v1" \
    MODEL_NAME="mock/mock-model" \
    MODEL_API_KEY="x" \
    JCLOUD_PREP_ONLY=1 \
    bash "$ENTRY" >"$TMP/run.out" 2>&1 || { cat "$TMP/run.out"; fail "entrypoint prep failed"; }
}

# --- run 1: fresh clone -----------------------------------------------------
run_prep
[ "$(git -C "$WS" rev-parse HEAD)" = "$COMMIT_A" ] || fail "run1 HEAD != commit A"
grep -q '"enabled": true' "$HOME_DIR/.jcode/config.json" || fail "memory not enabled in persistent config.json"
pass "run 1: fresh clone at commit A, memory enabled"

# Plant a .git marker (survives reuse, dies on re-clone), an untracked pollution
# file (must be cleaned), and a memory file under $HOME/.jcode (must be preserved).
touch "$WS/.git/REUSE_MARKER"
echo junk > "$WS/POLLUTION.txt"
echo "dirty" >> "$WS/file.txt"                      # dirty tracked change
mkdir -p "$HOME_DIR/.jcode/memory"
echo "remembered" > "$HOME_DIR/.jcode/memory/project.md"
# Simulate a prior run's session transcript (D12 hygiene: should be scrubbed
# before the next run to avoid cross-trust-boundary prompt/PII leakage).
mkdir -p "$HOME_DIR/.jcode/sessions"
echo '{"prompt":"secret-from-prior-run"}' > "$HOME_DIR/.jcode/sessions/abc-123.json"

# Plant a MALICIOUS git hook (security test): if hooks are NOT disabled, the
# reuse path's `git checkout`/`fetch` would trigger post-checkout and write a
# sentinel. With core.hooksPath=/dev/null + rm -rf .git/hooks, it must NOT fire.
mkdir -p "$WS/.git/hooks"
cat > "$WS/.git/hooks/post-checkout" <<'HOOK'
#!/usr/bin/env bash
touch "$WS/.git/HOOK_FIRED"
HOOK
chmod +x "$WS/.git/hooks/post-checkout"

# --- advance origin to commit B ---------------------------------------------
echo "v2" > "$SEED/file.txt"
git -C "$SEED" commit -qam "B" && git -C "$SEED" push -q origin main
COMMIT_B="$(git -C "$SEED" rev-parse HEAD)"
[ "$COMMIT_A" != "$COMMIT_B" ] || fail "A and B are the same commit"

# --- run 2: must REUSE ------------------------------------------------------
run_prep
grep -q "reused=1" "$TMP/run.out" || { cat "$TMP/run.out"; fail "run2 did not report reuse"; }
[ -f "$WS/.git/REUSE_MARKER" ] || fail "run2 re-cloned (marker gone) instead of reusing"
[ "$(git -C "$WS" rev-parse HEAD)" = "$COMMIT_B" ] || fail "run2 HEAD != commit B (did not fetch latest)"
[ ! -f "$WS/POLLUTION.txt" ] || fail "run2 left prior untracked pollution (clean -fdx failed)"
grep -q "^v2$" "$WS/file.txt" || fail "run2 did not reset dirty tracked change to origin"
[ -f "$HOME_DIR/.jcode/memory/project.md" ] || fail "run2 wiped jcode memory (must be preserved)"
# Security: the planted hook must NOT have fired during the reuse git operations.
[ ! -f "$WS/.git/HOOK_FIRED" ] || fail "SECURITY: git hook fired during reuse (hooks not disabled)"
# Session transcript scrubbed (D12 hygiene), memory preserved.
[ ! -f "$HOME_DIR/.jcode/sessions/abc-123.json" ] || fail "prior run's session transcript leaked (sessions not scrubbed)"
pass "run 2: reused checkout, fetched B, cleaned pollution, kept memory, hooks disabled, sessions scrubbed"

echo
pass "persistent-workspace reuse: all checks green"
