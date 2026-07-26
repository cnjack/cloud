-- 0048_run_autonomous_inputs: immutable, user-selected launch inputs.
-- File attachments deliberately are not represented here. They require the
-- object-store staging/binding contract, rather than database-encoded files.

ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS base_branch TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS model_effort TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS goal_mode BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE runs
    ADD CONSTRAINT runs_model_effort_check
    CHECK (model_effort IN ('', 'low', 'medium', 'high'));
