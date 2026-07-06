-- 0002_event_seq_alloc: fix the (run_id, seq) collision hazard.
--
-- BEFORE: the runner and the orchestrator's internal emitters (run.status,
-- run.artifact, run.failure) both wrote run_events with a client-chosen `seq`,
-- deduped first-writer-wins by UNIQUE(run_id, seq). A runner event and an
-- internal event could pick the same seq; the loser was SILENTLY DROPPED.
--
-- AFTER: `seq` is allocated SERVER-SIDE per run (a per-run monotonic counter,
-- assigned transactionally under a row lock on runs). The client-supplied number
-- is demoted to a per-SOURCE idempotency key (`client_seq`), so re-sending a
-- batch is still safe but no longer competes for the global `seq` space.
--
--   * source='runner'   → the runner's own seq (its 1..N stream)
--   * source='internal' → orchestrator-emitted events (client_seq mirrors seq)
--
-- The SSE contract to the console is unchanged: `seq` is still a per-run
-- monotonic integer starting at 1. Only its authority moved to the server.

ALTER TABLE run_events
    ADD COLUMN IF NOT EXISTS source     TEXT   NOT NULL DEFAULT 'internal',
    ADD COLUMN IF NOT EXISTS client_seq BIGINT NOT NULL DEFAULT 0;

-- Existing rows predate per-source dedupe: backfill client_seq = seq so the
-- dedupe key is well-formed and unique for them (they are all 'internal').
UPDATE run_events SET client_seq = seq WHERE client_seq = 0;

-- Per-source idempotency: a given (run, source, client_seq) may appear once.
-- This is what makes runner batch re-sends idempotent without colliding on the
-- global seq. Internal emitters set client_seq = the allocated seq, so they are
-- unique among themselves too.
CREATE UNIQUE INDEX IF NOT EXISTS run_events_dedupe_idx
    ON run_events (run_id, source, client_seq);
