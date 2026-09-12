-- Persist wall-clock deadlines across Orchestrator restarts and VM pauses.
ALTER TABLE cube_runtime_jobs ADD COLUMN IF NOT EXISTS timeout_seconds BIGINT NOT NULL DEFAULT 0;
