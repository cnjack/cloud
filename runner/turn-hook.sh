#!/usr/bin/env bash
# turn-hook.sh — per-turn (and single-shot / session-finalize) diff → commit →
# bundle/upload logic, extracted from entrypoint.sh (F7a / D22) so the SAME
# code path backs both:
#
#   * the single-shot run (RUN_SESSION unset): entrypoint.sh calls this once,
#     with TURN_HOOK_FINALIZE=1, right after the one-and-only turn — this is
#     EXACTLY the pre-F7a inline behavior, just moved into its own file.
#   * the multi-turn session loop (RUN_SESSION=1, docs/14-cloud-v2-design.md
#     §3 / docs/02-decision-log.md D22): acpdrive (runner/acpdrive/session.go)
#     execs this SYNCHRONOUSLY after every turn via --turn-hook
#     (TURN_HOOK_FINALIZE unset/0 — "do the turn's git work, never report a
#     run-level result"), and entrypoint.sh execs it ONE more time after the
#     whole session ends (TURN_HOOK_FINALIZE=1 — "the run is over: report
#     no_changes if nothing was EVER produced, across any turn").
#
# It is standalone/self-contained (its own log/die/report_* helpers) because
# acpdrive execs it as an independent subprocess with no shared shell state
# with entrypoint.sh (see runner/acpdrive/session.go's runTurnHook).
#
# Inputs (env):
#   From entrypoint.sh's Runner contract env + the `export` it does right
#   after computing BASE_REF:
#     WORKSPACE, OUT_DIR, RUN_ID, GIT_MODE, BRANCH_NAME, BASE_BRANCH,
#     BASE_REF, TASK_PROMPT, ORCH_BASE_URL, RUN_TOKEN
#   From acpdrive, session mode only (the turn-hook contract):
#     TURN_INDEX        1-based turn number
#     ACP_SESSION_ID     the ACP session id
#     ACP_STOP_REASON    the ACP stop reason for this turn
#   Set ONLY by entrypoint.sh's own direct (non-acpdrive) invocations:
#     TURN_HOOK_FINALIZE=1
#
# Exit code: 0 = ok (including "nothing to do this turn"); non-zero = fatal —
# in session mode acpdrive treats this exactly like an ACP transport error and
# aborts the whole run (see the turn-hook contract in session.go).
#
# Per-turn semantics (docs/14-cloud-v2-design.md §3): the diff computed below
# is always CUMULATIVE vs BASE_REF — the workspace is NEVER reset between
# turns, so each turn's changes accumulate on top of the last. A small marker
# file under $WORKSPACE/.git/ (never part of the tracked worktree, so never
# part of $DIFF itself) remembers the last diff this script actually acted on,
# so a turn that adds nothing NEW (identical cumulative diff to the previous
# invocation) skips the commit/upload silently instead of re-pushing identical
# content every turn. entrypoint.sh clears this marker at the start of every
# run (including a reused persistent workspace) so no state ever leaks across
# runs — see the PERSISTENT_WORKSPACE / cross-run hygiene comment there.
#
# Known F7b integration limitation (see the F7a task report): the bundle
# upload endpoint (POST .../bundle) and the diff artifact endpoint (POST
# .../artifact) were both designed for a SINGLE call per run. In session mode
# this script may call them multiple times over the life of one run (once per
# turn that produces new changes). The dedup above keeps that to "once per
# turn that actually changed something" rather than every turn, and both
# uploads are already treated as best-effort/non-fatal (a failed upload logs
# and continues — see below), so a server that rejects a second call for the
# same run cannot fail the run; it can only mean the LATEST turn's changes
# fail to show up until F7b makes these endpoints upsert-safe.

set -euo pipefail

log()  { printf '[turn-hook] %s\n' "$*" >&2; }

# report_failure / report_result / die mirror entrypoint.sh's own helpers
# (kept in sync intentionally; duplicated rather than sourced so this script
# stays a fully standalone executable — see the file header).
report_failure() {
  local reason="$1" message="$2"
  if command -v orchclient >/dev/null 2>&1; then
    orchclient report-failure --reason "$reason" --message "$message" || true
  fi
}

report_result() {
  local outcome="$1"
  if command -v orchclient >/dev/null 2>&1; then
    orchclient report-result --outcome "$outcome" || true
  fi
}

# die REASON MESSAGE — log and exit non-zero. Only the FINALIZE invocation
# (called directly by entrypoint.sh, whose `set -e` would otherwise propagate
# our non-zero exit WITHOUT any run.failure being posted) reports the failure
# itself. A per-turn invocation (execed by acpdrive) deliberately does NOT:
# acpdrive turns the non-zero exit into a fatal run error, acpdrive exits
# non-zero, and entrypoint.sh's "headless agent run failed" die reports
# agent_error exactly ONCE — reporting here too would double-post run.failure
# for the same underlying failure.
die() {
  local reason message
  if [ "$#" -ge 2 ]; then
    reason="$1"; message="$2"
  else
    reason="agent_error"; message="$1"
  fi
  printf '[turn-hook] ERROR: %s\n' "$message" >&2
  if [ "${FINALIZE:-0}" = "1" ]; then
    report_failure "$reason" "$message"
  fi
  exit 1
}

WORKSPACE="${WORKSPACE:?turn-hook.sh requires WORKSPACE}"
OUT_DIR="${OUT_DIR:-/out}"
RUN_ID="${RUN_ID:-}"
GIT_MODE="${GIT_MODE:-readonly}"
BASE_BRANCH="${BASE_BRANCH:-}"
BASE_REF="${BASE_REF:-}"
TASK_PROMPT="${TASK_PROMPT:-}"
TURN_INDEX="${TURN_INDEX:-1}"
ACP_SESSION_ID="${ACP_SESSION_ID:-}"
ACP_STOP_REASON="${ACP_STOP_REASON:-}"
FINALIZE="${TURN_HOOK_FINALIZE:-0}"

# Two pieces of cross-turn state, both under .git/ (never part of the tracked
# worktree, so never part of $DIFF itself), both cleared by entrypoint.sh at
# the start of every run so nothing leaks across runs on a persistent PVC:
#   STATE_FILE     the last cumulative diff this script actually acted on
#                  (per-turn dedup — see the header comment).
#   BUNDLE_MARKER  exists iff at least one bundle upload SUCCEEDED this run.
#                  Guards the finalize no_changes report below: once a bundle
#                  reached the orchestrator, the run branch exists server-side
#                  even if a later turn reverted every change — reporting
#                  no_changes then would contradict the pushed branch.
STATE_FILE="$WORKSPACE/.git/jcode-turn-hook.last-diff"
BUNDLE_MARKER="$WORKSPACE/.git/jcode-bundle-uploaded"

if [ "$FINALIZE" = "1" ]; then
  log "finalize (turn=$TURN_INDEX, run/session end)"
else
  log "[turn $TURN_INDEX] running (session=$ACP_SESSION_ID stop_reason=$ACP_STOP_REASON)"
fi

# --- compute the CUMULATIVE diff vs BASE_REF ---------------------------------
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

# Empty diff → first-class "no changes" outcome (D18), but ONLY reported at
# finalize time: a mid-session turn with nothing to show is a normal "pure
# conversation" turn, not a run-level result — reporting it here would be
# premature (a LATER turn may still produce changes) and would race the
# eventual real outcome. See docs/02-decision-log.md D22 / D18.
#
# And even at finalize, only when NO bundle ever reached the orchestrator
# (BUNDLE_MARKER absent): an earlier turn may have committed + uploaded a
# bundle and a later turn reverted everything, leaving the CUMULATIVE diff
# empty here while the pushed run branch (draft PR) still exists server-side.
# Reporting no_changes then would contradict the visible branch — so with the
# marker present we just finish as an ordinary success and let the uploaded
# bundle/artifact speak for the run's outcome.
if [ -z "$DIFF" ]; then
  if [ "$FINALIZE" = "1" ]; then
    if [ -e "$BUNDLE_MARKER" ]; then
      log "cumulative diff is empty but a bundle was uploaded earlier this run (changes pushed, later reverted) — NOT reporting no_changes"
    else
      log "no changes across the whole run — reporting no_changes, exit 0"
      report_result no_changes
    fi
  else
    log "[turn $TURN_INDEX] cumulative diff is empty — nothing to do this turn"
  fi
  log "success (no changes)"
  exit 0
fi

# --- per-turn dedup: skip commit/upload if nothing NEW since the last call --
PREV_DIFF=""
[ -f "$STATE_FILE" ] && PREV_DIFF="$(cat "$STATE_FILE" 2>/dev/null || true)"
if [ "$DIFF" = "$PREV_DIFF" ]; then
  log "[turn $TURN_INDEX] no NEW change since the last upload — skipping commit/upload"
  exit 0
fi

# --- draft-PR bundle (blueprint §3) ------------------------------------------
# In draft_pr mode we commit the agent's change onto BRANCH_NAME and build a
# git bundle (BASE_REF..BRANCH_NAME), then POST it to the orchestrator, which
# pushes the branch and opens/updates the draft PR. The runner NEVER pushes
# and holds NO token. readonly mode skips this entirely (diff-only).
if [ "$GIT_MODE" = "draft_pr" ]; then
  BRANCH_NAME="${BRANCH_NAME:-jcode/run-$RUN_ID}"
  [ -n "$BASE_REF" ] || die setup_failed "draft_pr requires a base commit but none was recorded"

  # Update mode (M7 webhook @mention task): BRANCH_NAME == BASE_BRANCH, i.e.
  # the agent builds ON an existing PR head branch and pushes back to it — we
  # are ALREADY on that branch after entrypoint.sh's checkout, so never try to
  # create it. Otherwise: turn 1 of a fresh draft_pr cuts BRANCH_NAME off the
  # base; turn 2+ (session mode) is already ON that branch (the workspace is
  # never reset between turns) — re-running `checkout -b` would fail because
  # the branch already exists, so detect and skip re-creation.
  CURRENT_BRANCH="$(git -C "$WORKSPACE" symbolic-ref --short -q HEAD || true)"
  if [ -n "$BASE_BRANCH" ] && [ "$BRANCH_NAME" = "$BASE_BRANCH" ]; then
    log "draft_pr update mode: committing agent changes onto existing branch $BRANCH_NAME (base $BASE_REF)"
  elif [ "$CURRENT_BRANCH" = "$BRANCH_NAME" ]; then
    log "draft_pr: [turn $TURN_INDEX] continuing on already-created branch $BRANCH_NAME"
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
  # BASE_REF..BRANCH_NAME is always CUMULATIVE (every commit since the true
  # base, not just this turn's), so each re-upload fully reconstructs the
  # branch — the orchestrator's bare clone already has BASE_REF, so
  # `git fetch <bundle>` then `git push` reconstructs/updates the branch.
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
    # Best-effort/non-fatal (as today): a failed upload never fails the run —
    # in session mode this ALSO means a later turn's successful upload isn't
    # blocked by an earlier turn's transient failure (see the F7b integration
    # note in this file's header). A SUCCESSFUL upload stamps BUNDLE_MARKER:
    # from that point on the run branch exists server-side, so the finalize
    # no_changes report is permanently suppressed for this run (see above).
    if orchclient upload-bundle --file "$RUN_BUNDLE"; then
      touch "$BUNDLE_MARKER" 2>/dev/null || true
    else
      log "bundle upload failed (non-fatal; the draft PR will not open/update until retried)"
    fi
  else
    log "no orchestrator wired; skipping bundle upload (standalone run)"
  fi
  rm -f "$RUN_BUNDLE" 2>/dev/null || true
fi

# --- upload the diff artifact (readonly AND draft_pr) ------------------------
if command -v orchclient >/dev/null 2>&1 && [ -n "${ORCH_BASE_URL:-}" ] && [ -n "${RUN_TOKEN:-}" ]; then
  if printf '%s\n' "$DIFF" | orchclient upload-artifact --kind diff --file - ; then
    log "uploaded diff artifact to orchestrator"
  else
    log "diff artifact upload failed (non-fatal; diff still in /out and stdout)"
  fi
fi

printf '%s' "$DIFF" > "$STATE_FILE" 2>/dev/null || true
log "success ([turn $TURN_INDEX] changes committed/uploaded)"
exit 0
