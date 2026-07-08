#!/usr/bin/env bash
# test-session-scrub-matrix.sh — proves F9a's session-transcript retention
# matrix in entrypoint.sh (step 3, D23 ①②): $HOME/.jcode/sessions is scrubbed
# before every run EXCEPT the one combination that needs its transcript to
# survive for a later ACP session/load resume — a SESSION_MODE run
# (RUN_SESSION=1, and not forced off by RUN_KIND=review) on a
# PERSISTENT_WORKSPACE=1 PVC.
#
# Runs entrypoint.sh DIRECTLY (bash, no docker), same trick as
# test-persistent-reuse.sh: SOURCE_MODE=clone + the test-only JCLOUD_PREP_ONLY=1
# hook stops the script right after workspace prep + the scrub matrix itself
# (step 3), BEFORE any jcode/acpdrive binary is needed — so this needs only
# `git` and `bash`. The RESUME_SESSION_ID wiring cases at the bottom go
# further: they run THROUGH step 4 against a PATH-injected fake acpdrive
# (recording argv + env) and fake orchclient, still with no docker/model.
#
# Matrix (mirrors the comment in entrypoint.sh step 3). The scrub/preserve
# DECISION only ever fires when PERSISTENT_WORKSPACE=1 — with
# PERSISTENT_WORKSPACE=0 the guard on both branches never matches (unchanged
# from before F9a: in production an ephemeral HOME never carries a leftover
# transcript in the first place, so this is a deliberate no-op, not a gap):
#
#   RUN_SESSION  PERSISTENT_WORKSPACE  RUN_KIND  outcome
#   *            0                     agent     no-op (neither branch matches; unchanged pre-F9a behavior)
#   0            1                     agent     scrubbed (D12 default)
#   1            1                     agent     preserved (D23 ①② resume substrate — the ONE new case)
#   1            1                     review    scrubbed (SESSION_MODE forced off for review, so the
#                                                D12 default applies despite RUN_SESSION=1 — proves the
#                                                gate is SESSION_MODE, not raw RUN_SESSION)
#
# Env: KEEP=1 keeps the scratch dir.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENTRY="$HERE/entrypoint.sh"

pass() { printf '\033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$*"; exit 1; }
info() { printf '[scrub-matrix-test] %s\n' "$*"; }

command -v git >/dev/null 2>&1 || fail "git is required"
[ -x "$ENTRY" ] || fail "entrypoint.sh not found/executable at $ENTRY"

TMP="$(mktemp -d)"
cleanup() { [ "${KEEP:-0}" = "1" ] && { info "KEEP=1, leaving $TMP"; return; }; rm -rf "$TMP"; }
trap cleanup EXIT

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

# --- a trivial "origin" repo (single commit on main) ------------------------
ORIGIN="$TMP/origin.git"
git init -q --bare "$ORIGIN"
SEED="$TMP/seed"
git init -q "$SEED"; git -C "$SEED" checkout -q -B main
echo "v1" > "$SEED/file.txt"
git -C "$SEED" add -A && git -C "$SEED" commit -q -m "A"
git -C "$SEED" remote add origin "$ORIGIN" && git -C "$SEED" push -q origin main

N=0
# run_case RUN_SESSION PERSISTENT_WORKSPACE RUN_KIND OUTCOME[scrubbed|preserved|noop]
run_case() {
  local run_session="$1" persistent="$2" run_kind="$3" outcome="$4"
  N=$((N + 1))
  local ws="$TMP/ws-$N" home="$TMP/home-$N"
  mkdir -p "$ws" "$home/.jcode/sessions"
  # Plant a fake prior-run transcript BEFORE this run — this is what the
  # matrix decides to scrub or preserve.
  echo '{"prompt":"secret-from-a-prior-run"}' > "$home/.jcode/sessions/prior-session.json"

  local extra_env=()
  if [ "$run_kind" = "review" ]; then
    extra_env=(RUN_KIND=review PR_HEAD=main PR_BASE=main)
  else
    extra_env=(RUN_KIND=agent)
  fi

  local out="$TMP/run-$N.out"
  set +e
  env -i PATH="$PATH" HOME="$home" \
    WORKSPACE="$ws" \
    PERSISTENT_WORKSPACE="$persistent" \
    RUN_SESSION="$run_session" \
    SOURCE_MODE=clone \
    REPO_URL="$ORIGIN" \
    BASE_BRANCH=main \
    "${extra_env[@]}" \
    TASK_PROMPT="do it" \
    MODEL_BASE_URL="http://model.invalid/v1" \
    MODEL_NAME="mock/mock-model" \
    MODEL_API_KEY="x" \
    JCLOUD_PREP_ONLY=1 \
    bash "$ENTRY" >"$out" 2>&1
  local rc=$?
  set -e
  [ "$rc" -eq 0 ] || { cat "$out"; fail "case $N (RUN_SESSION=$run_session PERSISTENT_WORKSPACE=$persistent RUN_KIND=$run_kind) exited $rc"; }

  local label="RUN_SESSION=$run_session PERSISTENT_WORKSPACE=$persistent RUN_KIND=$run_kind"
  case "$outcome" in
    scrubbed)
      [ ! -f "$home/.jcode/sessions/prior-session.json" ] \
        || { cat "$out"; fail "[$label] sessions/ NOT scrubbed, want scrubbed"; }
      grep -q "scrubbed \$HOME/.jcode/sessions" "$out" \
        || { cat "$out"; fail "[$label] missing the 'scrubbed' log line"; }
      pass "[$label] sessions/ scrubbed (D12 default)"
      ;;
    preserved)
      [ -f "$home/.jcode/sessions/prior-session.json" ] \
        || { cat "$out"; fail "[$label] sessions/ WAS scrubbed, want preserved (D23 ①② resume substrate)"; }
      grep -q "preserving \$HOME/.jcode/sessions" "$out" \
        || { cat "$out"; fail "[$label] missing the 'preserving' log line"; }
      pass "[$label] sessions/ preserved (D23 ①② resume substrate)"
      ;;
    noop)
      # PERSISTENT_WORKSPACE=0: neither branch's guard matches (unchanged
      # from before F9a) — no scrub/preserve log line either way. We do NOT
      # assert on the planted file's survival: it is a test-harness artifact
      # only (a real ephemeral HOME never has one), not a red-line contract.
      if grep -q "scrubbed \$HOME/.jcode/sessions\|preserving \$HOME/.jcode/sessions" "$out"; then
        cat "$out"; fail "[$label] expected NO scrub/preserve decision (PERSISTENT_WORKSPACE=0), but one fired"
      fi
      pass "[$label] no scrub/preserve decision made (PERSISTENT_WORKSPACE=0, unchanged pre-F9a no-op)"
      ;;
    *) fail "bad test outcome spec: $outcome" ;;
  esac
}

info "matrix: RUN_SESSION x PERSISTENT_WORKSPACE x RUN_KIND -> \$HOME/.jcode/sessions decision"
run_case 0 0 agent  noop
run_case 1 0 agent  noop
run_case 0 1 agent  scrubbed
run_case 1 1 agent  preserved
run_case 1 1 review scrubbed   # SESSION_MODE forced off for review -> D12 default applies

# === RESUME_SESSION_ID wiring through step 4 (fake acpdrive) =================
# These cases run entrypoint.sh all the way THROUGH step 4 (no
# JCLOUD_PREP_ONLY): a PATH-injected fake `acpdrive` records its argv and its
# process environment and exits 0, and a fake `orchclient` (same pattern as
# test-turn-hook.sh) absorbs the finalize turn-hook's uploads/reports. This
# proves the actual acpdrive invocation, not just the prep-phase logging:
#
#   a) single-shot + stale RESUME_SESSION_ID in the pod env (CONFIRMED-1):
#      WARNING logged, run SUCCEEDS, NO --resume in acpdrive's argv, and
#      RESUME_SESSION_ID absent from acpdrive's child environment (the
#      entrypoint scrubs it; acpdrive's --resume has no env fallback anyway —
#      defense in depth).
#   b) session mode + RESUME_SESSION_ID (the warm-wake path): --resume with
#      the exact id IS passed, alongside --session.
FAKEBIN="$TMP/fakebin"
mkdir -p "$FAKEBIN"
cat > "$FAKEBIN/acpdrive" <<'FAKE'
#!/bin/sh
[ -t 0 ] || cat >/dev/null
printf '%s\n' "$*" > "$ACPDRIVE_ARGV_LOG"
env > "$ACPDRIVE_ENV_LOG"
exit 0
FAKE
cat > "$FAKEBIN/orchclient" <<'FAKE'
#!/bin/sh
[ -t 0 ] || cat >/dev/null
exit 0
FAKE
chmod +x "$FAKEBIN/acpdrive" "$FAKEBIN/orchclient"

# run_step4 NAME RUN_SESSION RESUME_ID — runs entrypoint through step 4 with
# the fake acpdrive; sets OUT/ARGV_LOG/ENV_LOG/RC globals for assertions.
run_step4() {
  local name="$1" run_session="$2" resume_id="$3"
  local ws="$TMP/ws-$name" home="$TMP/home-$name" out_dir="$TMP/outdir-$name"
  mkdir -p "$ws" "$home" "$out_dir"
  OUT="$TMP/run-$name.out"
  ARGV_LOG="$TMP/acpdrive-argv-$name.log"
  ENV_LOG="$TMP/acpdrive-env-$name.log"
  set +e
  env -i PATH="$FAKEBIN:$PATH" HOME="$home" \
    ACPDRIVE_ARGV_LOG="$ARGV_LOG" \
    ACPDRIVE_ENV_LOG="$ENV_LOG" \
    WORKSPACE="$ws" \
    OUT_DIR="$out_dir" \
    PERSISTENT_WORKSPACE=0 \
    RUN_SESSION="$run_session" \
    RESUME_SESSION_ID="$resume_id" \
    SOURCE_MODE=clone \
    REPO_URL="$ORIGIN" \
    BASE_BRANCH=main \
    RUN_KIND=agent \
    TASK_PROMPT="do it" \
    MODEL_BASE_URL="http://model.invalid/v1" \
    MODEL_NAME="mock/mock-model" \
    MODEL_API_KEY="x" \
    ORCH_BASE_URL="http://fake-orch.test" \
    RUN_TOKEN="fake-token" \
    bash "$ENTRY" >"$OUT" 2>&1
  RC=$?
  set -e
}

info "[a] single-shot + stale RESUME_SESSION_ID env: warn, scrub, session/new path"
run_step4 warn 0 sess_orphaned
[ "$RC" -eq 0 ] || { cat "$OUT"; fail "[a] single-shot run with a stale RESUME_SESSION_ID exited $RC (CONFIRMED-1: it must succeed on session/new, not die on session/load)"; }
grep -q "WARNING: RESUME_SESSION_ID=sess_orphaned set but session mode is off" "$OUT" \
  || { cat "$OUT"; fail "[a] missing the RESUME_SESSION_ID mismatch WARNING log line"; }
[ -s "$ARGV_LOG" ] || { cat "$OUT"; fail "[a] fake acpdrive never ran (step 4 not reached)"; }
if grep -q -- "--resume" "$ARGV_LOG"; then
  cat "$ARGV_LOG"; fail "[a] --resume leaked into the single-shot acpdrive argv"
fi
if grep -q "^RESUME_SESSION_ID=" "$ENV_LOG"; then
  grep "^RESUME_SESSION_ID=" "$ENV_LOG"; fail "[a] RESUME_SESSION_ID leaked into acpdrive's child environment (entrypoint must scrub it)"
fi
grep -q "resume=<none>" "$OUT" \
  || { cat "$OUT"; fail "[a] step-4 log line should show resume=<none> after the scrub"; }
pass "[a] single-shot + stale env id: WARNING, no --resume in argv, env scrubbed, run succeeded"

info "[b] session mode + RESUME_SESSION_ID: --resume passed through"
run_step4 wake 1 sess_wake_42
[ "$RC" -eq 0 ] || { cat "$OUT"; fail "[b] session-mode resume run exited $RC"; }
grep -q -- "--session" "$ARGV_LOG" \
  || { cat "$ARGV_LOG"; fail "[b] --session missing from acpdrive argv"; }
grep -q -- "--resume sess_wake_42" "$ARGV_LOG" \
  || { cat "$ARGV_LOG"; fail "[b] --resume sess_wake_42 missing from acpdrive argv"; }
grep -q "resume=sess_wake_42" "$OUT" \
  || { cat "$OUT"; fail "[b] step-4 log line should show resume=sess_wake_42"; }
pass "[b] session mode: --resume sess_wake_42 passed to acpdrive explicitly"

echo
printf '\033[32m=====================================================\033[0m\n'
printf '\033[32m  PROVEN: session-transcript scrub matrix (D23 ①②)   \033[0m\n'
printf '\033[32m=====================================================\033[0m\n'
