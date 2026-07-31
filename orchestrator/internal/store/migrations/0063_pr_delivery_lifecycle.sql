-- 0063: lifecycle-aware pull-request delivery.
-- Existing services keep the historical always-draft behavior; new API writes
-- opt into lifecycle_aware explicitly. Run state is nullable so old/human PRs
-- are shown as unknown rather than guessed to be drafts.
ALTER TABLE services
    ADD COLUMN IF NOT EXISTS pr_ready_policy TEXT NOT NULL DEFAULT 'always_draft';

DO $$ BEGIN
    ALTER TABLE services ADD CONSTRAINT services_pr_ready_policy_check
        CHECK (pr_ready_policy IN ('always_draft','lifecycle_aware'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS pr_draft   BOOLEAN,
    ADD COLUMN IF NOT EXISTS pr_ready_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS pr_state TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS pr_ready_policy TEXT NOT NULL DEFAULT 'always_draft',
    ADD COLUMN IF NOT EXISTS pr_create_claim_token TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS pr_create_claimed_at TIMESTAMPTZ;

DO $$ BEGIN
    ALTER TABLE runs ADD CONSTRAINT runs_pr_ready_policy_check
        CHECK (pr_ready_policy IN ('always_draft','lifecycle_aware'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE INDEX IF NOT EXISTS runs_session_pr_ready_idx
    ON runs (created_at)
    WHERE session AND status='succeeded' AND pr_draft IS TRUE AND pr_ready_at IS NULL;
