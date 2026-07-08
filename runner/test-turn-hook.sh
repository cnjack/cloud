#!/usr/bin/env bash
# test-turn-hook.sh — shell-level proof of runner/turn-hook.sh (F7a / D22), the
# per-turn diff → commit → bundle/upload logic extracted from entrypoint.sh.
# Pure local git fixtures — NO docker, NO orchestrator: orchclient is replaced
# by a PATH-injected fake that records every invocation to a log the
# assertions grep.
#
# Scenarios:
#   A. draft_pr session lifecycle: turn 1 commits + uploads; turn 2 with no new
#      change dedup-skips; turn 3 reuses the already-created branch and
#      re-uploads the cumulative bundle; finalize dedup-skips and reports NO
#      result (changes exist).
#   B. REVERT (regression for the confirmed finalize-no_changes bug): turn 1
#      uploads a bundle, turn 2 reverts everything → finalize sees an empty
#      cumulative diff but MUST NOT report no_changes (a pushed run branch
#      exists server-side).
#   C. pure conversation: no turn ever changes anything → per-turn hook stays
#      silent, finalize reports no_changes EXACTLY once.
#   D. readonly mode: diff artifact only — no bundle, no branch, no marker;
#      dedup applies the same way.
#   E. BASE_REF empty: falls back to plain `git diff` and still produces the
#      diff artifact.
#   F. failure reporting (P3): a per-turn die exits non-zero WITHOUT posting
#      run.failure itself (acpdrive→entrypoint reports once); the SAME failure
#      under FINALIZE=1 posts run.failure (entrypoint's set -e path would
#      otherwise swallow the reason).
#
# Env: KEEP=1 to keep the temp dir for inspection.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOOK="$HERE/turn-hook.sh"

pass() { printf '\033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$*"; dump; cleanup; exit 1; }
info() { printf '[hook-test] %s\n' "$*"; }

dump() {
  echo "----- orchclient call log -----"; cat "${ORCH_LOG:-/dev/null}" 2>/dev/null || true
  echo "----- last hook output -----"; cat "${TMP:-/dev/null}/hook.out" 2>/dev/null || true
}
cleanup() {
  [ "${KEEP:-0}" = "1" ] && { info "KEEP=1, leaving $TMP"; return; }
  [ -n "${TMP:-}" ] && rm -rf "$TMP" 2>/dev/null || true
}
trap cleanup EXIT

need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }
need git; need bash
[ -x "$HOOK" ] || fail "turn-hook.sh not found/executable at $HOOK"

TMP="$(mktemp -d)"

# === fake orchclient (PATH injection) =========================================
# Records every invocation (one argv per line) to $ORCH_LOG and exits 0. Drains
# stdin when piped (upload-artifact --file -) so the hook's pipe never blocks.
FAKEBIN="$TMP/bin"
mkdir -p "$FAKEBIN"
cat > "$FAKEBIN/orchclient" <<'FAKE'
#!/bin/sh
[ -t 0 ] || cat >/dev/null
echo "$@" >> "$ORCH_LOG"
exit 0
FAKE
chmod +x "$FAKEBIN/orchclient"
export PATH="$FAKEBIN:$PATH"
export ORCH_BASE_URL="http://fake-orch.test" RUN_TOKEN="fake-token"

# === helpers ==================================================================

# new_fixture NAME — fresh git repo + out dir + orchclient log; sets WS, OUT,
# ORCH_LOG, BASE (the base commit sha) globals.
new_fixture() {
  local name="$1"
  WS="$TMP/ws-$name"; OUT="$TMP/out-$name"; ORCH_LOG="$TMP/orch-$name.log"
  export ORCH_LOG
  mkdir -p "$WS" "$OUT"; : > "$ORCH_LOG"
  (
    cd "$WS"; git init -q -b main
    git config user.email seed@jcode.local; git config user.name seed
    printf '# seed\n' > README.md; git add -A; git commit -qm init
  )
  BASE="$(git -C "$WS" rev-parse HEAD)"
}

# run_hook TURN FINALIZE GIT_MODE BASE_REF [BRANCH_NAME] — invoke the hook with
# the fixture env; captures combined output in $TMP/hook.out; returns its rc.
run_hook() {
  local turn="$1" finalize="$2" git_mode="$3" base_ref="$4" branch="${5:-}"
  set +e
  WORKSPACE="$WS" OUT_DIR="$OUT" RUN_ID="$RUN_ID" GIT_MODE="$git_mode" \
    BASE_REF="$base_ref" BASE_BRANCH="main" BRANCH_NAME="$branch" \
    TASK_PROMPT="test task" TURN_INDEX="$turn" ACP_SESSION_ID="sess_t" \
    ACP_STOP_REASON="end_turn" TURN_HOOK_FINALIZE="$finalize" \
    "$HOOK" > "$TMP/hook.out" 2>&1
  HOOK_RC=$?
  set -e
  return 0
}

count_log() { grep -c -- "$1" "$ORCH_LOG" 2>/dev/null || true; }

# === A. draft_pr session lifecycle ===========================================
info "[A] draft_pr lifecycle: commit+upload / dedup / branch reuse / finalize"
new_fixture A; RUN_ID="hook-a"; BR="jcode/run-hook-a"

echo "change one" > "$WS/one.txt"
run_hook 1 0 draft_pr "$BASE" "$BR"
[ "$HOOK_RC" -eq 0 ] || fail "[A] turn 1 exited $HOOK_RC (want 0)"
[ "$(count_log upload-bundle)" = "1" ] || fail "[A] turn 1: want exactly 1 upload-bundle, got $(count_log upload-bundle)"
[ "$(count_log upload-artifact)" = "1" ] || fail "[A] turn 1: want exactly 1 upload-artifact"
git -C "$WS" rev-parse --verify -q "refs/heads/$BR" >/dev/null || fail "[A] turn 1 did not create branch $BR"
[ "$(git -C "$WS" rev-list --count "$BASE..$BR")" = "1" ] || fail "[A] turn 1: want 1 commit on $BR"
[ -e "$WS/.git/jcode-bundle-uploaded" ] || fail "[A] turn 1: bundle-uploaded marker missing after successful upload"
[ -f "$WS/.git/jcode-turn-hook.last-diff" ] || fail "[A] turn 1: dedup state file missing"
pass "[A] turn 1: committed onto $BR, uploaded bundle+artifact, markers set"

run_hook 2 0 draft_pr "$BASE" "$BR"
[ "$HOOK_RC" -eq 0 ] || fail "[A] turn 2 exited $HOOK_RC (want 0)"
[ "$(count_log upload-bundle)" = "1" ] || fail "[A] turn 2: dedup failed — a second bundle was uploaded with no new change"
grep -q "no NEW change" "$TMP/hook.out" || fail "[A] turn 2: expected the dedup skip log line"
pass "[A] turn 2: no new change — dedup skipped commit/upload"

echo "change two" > "$WS/two.txt"
run_hook 3 0 draft_pr "$BASE" "$BR"
[ "$HOOK_RC" -eq 0 ] || fail "[A] turn 3 exited $HOOK_RC (want 0)"
[ "$(count_log upload-bundle)" = "2" ] || fail "[A] turn 3: want 2 upload-bundle calls total"
grep -q "continuing on already-created branch" "$TMP/hook.out" || fail "[A] turn 3: expected branch-reuse log (checkout -b must not rerun)"
[ "$(git -C "$WS" rev-list --count "$BASE..$BR")" = "2" ] || fail "[A] turn 3: want 2 commits on $BR"
pass "[A] turn 3: new change committed onto the SAME branch, cumulative bundle re-uploaded"

run_hook 3 1 draft_pr "$BASE" "$BR"
[ "$HOOK_RC" -eq 0 ] || fail "[A] finalize exited $HOOK_RC (want 0)"
[ "$(count_log upload-bundle)" = "2" ] || fail "[A] finalize: must dedup-skip (diff unchanged since turn 3)"
[ "$(count_log report-result)" = "0" ] || fail "[A] finalize: must NOT report a result when changes exist"
pass "[A] finalize: dedup skip, no result reported (changes speak for themselves)"

# === B. revert scenario (finalize must NOT report no_changes) =================
info "[B] revert: bundle uploaded then all changes reverted"
new_fixture B; RUN_ID="hook-b"; BR="jcode/run-hook-b"

echo "temp change" > "$WS/temp.txt"
run_hook 1 0 draft_pr "$BASE" "$BR"
[ "$HOOK_RC" -eq 0 ] || fail "[B] turn 1 exited $HOOK_RC"
[ "$(count_log upload-bundle)" = "1" ] || fail "[B] turn 1: bundle not uploaded"
[ -e "$WS/.git/jcode-bundle-uploaded" ] || fail "[B] turn 1: marker missing"

rm "$WS/temp.txt"   # the agent reverts everything → cumulative diff vs BASE is empty
run_hook 2 0 draft_pr "$BASE" "$BR"
[ "$HOOK_RC" -eq 0 ] || fail "[B] turn 2 (revert) exited $HOOK_RC"
[ "$(count_log report-result)" = "0" ] || fail "[B] turn 2: per-turn hook must never report a result"

run_hook 2 1 draft_pr "$BASE" "$BR"
[ "$HOOK_RC" -eq 0 ] || fail "[B] finalize exited $HOOK_RC"
[ "$(count_log report-result)" = "0" ] || fail "[B] finalize reported a result despite an uploaded bundle (no_changes would contradict the pushed branch!)"
grep -q "NOT reporting no_changes" "$TMP/hook.out" || fail "[B] finalize: expected the explicit marker-suppression log line"
pass "[B] revert: finalize suppressed no_changes (bundle marker present)"

# === C. pure conversation: no_changes reported exactly once at finalize ======
info "[C] pure conversation run"
new_fixture C; RUN_ID="hook-c"

run_hook 1 0 draft_pr "$BASE" "jcode/run-hook-c"
[ "$HOOK_RC" -eq 0 ] || fail "[C] turn 1 exited $HOOK_RC"
[ "$(count_log report-result)" = "0" ] || fail "[C] per-turn hook reported a result mid-session"

run_hook 2 0 draft_pr "$BASE" "jcode/run-hook-c"
[ "$HOOK_RC" -eq 0 ] || fail "[C] turn 2 exited $HOOK_RC"
[ "$(count_log report-result)" = "0" ] || fail "[C] per-turn hook reported a result mid-session (turn 2)"

run_hook 2 1 draft_pr "$BASE" "jcode/run-hook-c"
[ "$HOOK_RC" -eq 0 ] || fail "[C] finalize exited $HOOK_RC"
[ "$(grep -c -- "report-result --outcome no_changes" "$ORCH_LOG")" = "1" ] \
  || fail "[C] finalize: want no_changes reported EXACTLY once, log: $(cat "$ORCH_LOG")"
[ "$(count_log upload-bundle)" = "0" ] || fail "[C] a bundle was uploaded for a no-change run"
pass "[C] pure conversation: silent per-turn, no_changes exactly once at finalize"

# === D. readonly mode: artifact only, no bundle/branch/marker ================
info "[D] readonly mode"
new_fixture D; RUN_ID="hook-d"

echo "ro change" > "$WS/ro.txt"
run_hook 1 0 readonly "$BASE"
[ "$HOOK_RC" -eq 0 ] || fail "[D] turn 1 exited $HOOK_RC"
[ "$(count_log upload-artifact)" = "1" ] || fail "[D] turn 1: diff artifact not uploaded"
[ "$(count_log upload-bundle)" = "0" ] || fail "[D] readonly uploaded a bundle"
[ "$(git -C "$WS" branch | wc -l | tr -d ' ')" = "1" ] || fail "[D] readonly created a branch"
[ ! -e "$WS/.git/jcode-bundle-uploaded" ] || fail "[D] readonly set the bundle marker"
[ -s "$OUT/diff.patch" ] || fail "[D] diff.patch missing/empty"
grep -q "===JCODE_DIFF_BEGIN" "$TMP/hook.out" || fail "[D] stdout diff markers missing"
pass "[D] readonly turn: artifact only (no bundle/branch/marker), diff.patch + stdout markers present"

run_hook 1 1 readonly "$BASE"
[ "$HOOK_RC" -eq 0 ] || fail "[D] finalize exited $HOOK_RC"
[ "$(count_log upload-artifact)" = "1" ] || fail "[D] finalize: dedup failed for readonly"
[ "$(count_log report-result)" = "0" ] || fail "[D] finalize reported a result despite changes"
pass "[D] readonly finalize: dedup skip, no result"

# === E. BASE_REF empty falls back to plain git diff ==========================
info "[E] empty BASE_REF fallback"
new_fixture E; RUN_ID="hook-e"

echo "fallback change" > "$WS/fb.txt"
run_hook 1 0 readonly ""
[ "$HOOK_RC" -eq 0 ] || fail "[E] hook exited $HOOK_RC"
grep -q "no BASE_REF recorded" "$TMP/hook.out" || fail "[E] fallback log line missing"
[ -s "$OUT/diff.patch" ] || fail "[E] diff.patch missing/empty via the fallback diff"
[ "$(count_log upload-artifact)" = "1" ] || fail "[E] artifact not uploaded via the fallback diff"
pass "[E] empty BASE_REF: plain-diff fallback produced and uploaded the diff"

# === F. failure reporting split (P3) ==========================================
# draft_pr with an empty BASE_REF is a deterministic die (setup_failed): the
# per-turn invocation must exit non-zero WITHOUT posting run.failure (the
# acpdrive→entrypoint chain reports once); the finalize invocation must post it
# (entrypoint's set -e would otherwise exit with no reason recorded).
info "[F] die reporting: per-turn silent, finalize reports"
new_fixture F; RUN_ID="hook-f"

echo "doomed change" > "$WS/doom.txt"
run_hook 1 0 draft_pr "" "jcode/run-hook-f"
[ "$HOOK_RC" -ne 0 ] || fail "[F] per-turn hook exited 0 for a draft_pr run without a base commit"
[ "$(count_log report-failure)" = "0" ] || fail "[F] per-turn die posted run.failure itself (double-report with entrypoint)"
pass "[F] per-turn die: non-zero exit, no self-report"

: > "$ORCH_LOG"
run_hook 1 1 draft_pr "" "jcode/run-hook-f"
[ "$HOOK_RC" -ne 0 ] || fail "[F] finalize hook exited 0 for a draft_pr run without a base commit"
[ "$(count_log report-failure)" = "1" ] || fail "[F] finalize die must post run.failure exactly once, log: $(cat "$ORCH_LOG")"
pass "[F] finalize die: non-zero exit + run.failure posted once"

echo
printf '\033[32m===========================================================\033[0m\n'
printf '\033[32m  PROVEN: turn-hook.sh per-turn/finalize semantics — dedup,  \033[0m\n'
printf '\033[32m  branch reuse, revert-safe no_changes, split failure report \033[0m\n'
printf '\033[32m===========================================================\033[0m\n'
