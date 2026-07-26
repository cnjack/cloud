-- 0054_run_coalesce_key: durable key for atomically coalescing bursty SCM Runs.

ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS coalesce_key TEXT NOT NULL DEFAULT '';

-- Store implementations cancel the prior queued row before inserting the next
-- one under a stable per-key lock. Keep a database constraint as a final guard
-- against regressions or mixed replicas violating the invariant.
CREATE UNIQUE INDEX IF NOT EXISTS runs_one_queued_per_coalesce_key_uq
    ON runs (coalesce_key)
    WHERE coalesce_key <> '' AND status = 'queued';
