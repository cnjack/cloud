-- 0014_session: multi-turn SESSION runs (D22; docs/14-cloud-v2-design.md §2/§3).
--
-- A session run keeps ONE ACP session alive across turns: the runner
-- (RUN_SESSION=1) loops session/prompt, the run parks in the new non-terminal
-- status 'awaiting_input' between turns, and the user feeds follow-up prompts
-- via POST /runs/{id}/messages. runs.status has no CHECK constraint (0001), so
-- 'awaiting_input' needs no enum widening — only the app state machine changed.
--
-- All additions are NOT NULL DEFAULT / nullable, so an old runner/console and a
-- populated DB keep working unchanged (a non-session run leaves every new column
-- at its default).

-- runs: the session flag + per-turn/idle/finalize bookkeeping.
--   session            — this run is a multi-turn session (only kind=agent).
--   awaiting_since      — stamped when the run enters awaiting_input; the idle
--                         reclaim epoch (cleared when a message resumes it).
--   session_finalizing  — set by the finish endpoint / idle-timeout pass so
--                         next-prompt answers 410 and the runner exits cleanly.
--   bundle_rev/pushed_rev — per-turn draft-PR push cursor: handleIngestBundle
--                         bumps bundle_rev, the session-push pass advances
--                         pushed_rev once that revision is pushed.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS session            BOOLEAN     NOT NULL DEFAULT FALSE;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS awaiting_since      TIMESTAMPTZ;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS session_finalizing  BOOLEAN     NOT NULL DEFAULT FALSE;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS bundle_rev          BIGINT      NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS pushed_rev          BIGINT      NOT NULL DEFAULT 0;

-- run_messages: the follow-up prompt DELIVERY QUEUE the runner drains via
-- next-prompt (NOT the chat transcript — each message is also a user.message
-- run_event for the timeline). seq is monotonic per run. Delivery is TWO-PHASE
-- (offer/consume) so a lost next-prompt response can never strand a message:
--   offered_at   — stamped when a next-prompt poll hands the message to the
--                  runner. While offered-but-not-consumed, every re-poll
--                  IDEMPOTENTLY re-delivers the SAME message (same id/prompt):
--                  acpdrive only polls between turns, so a re-poll proves the
--                  previous response never started a turn.
--   consumed_at  — stamped by the NEXT turn-complete: the turn this message
--                  started has finished, the delivery is done. Only then does
--                  the next queued message become offerable.
CREATE TABLE IF NOT EXISTS run_messages (
    id           TEXT        PRIMARY KEY,
    run_id       TEXT        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    seq          BIGINT      NOT NULL,
    prompt       TEXT        NOT NULL,
    created_by   TEXT        REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    offered_at   TIMESTAMPTZ,
    consumed_at  TIMESTAMPTZ,
    UNIQUE (run_id, seq)
);

-- Fast queue scans: the pending (unconsumed) slice of a run's queue — covers
-- both the re-offer lookup (offered, unconsumed) and the next-offer pick
-- (unoffered).
CREATE INDEX IF NOT EXISTS run_messages_pending_idx
    ON run_messages (run_id, seq)
    WHERE consumed_at IS NULL;

-- projects: session guardrails (D22). NULL = inherit the cluster default
-- (MAX_LIVE_SESSIONS / SESSION_IDLE_TIMEOUT_SECONDS / SESSION_TTL_SECONDS),
-- mirroring the D15 presence semantics of the other guardrails.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS max_live_sessions         INT;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS session_idle_timeout_secs BIGINT;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS session_ttl_secs          BIGINT;
