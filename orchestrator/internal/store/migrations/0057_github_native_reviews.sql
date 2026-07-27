-- 0057_github_native_reviews: make review semantics explicit in the shared
-- Plugin Automation pipeline and retain validated structured review output.

ALTER TABLE automations_v2
    ADD COLUMN IF NOT EXISTS run_kind TEXT NOT NULL DEFAULT 'agent';

ALTER TABLE automations_v2
    DROP CONSTRAINT IF EXISTS automations_v2_run_kind_check;
ALTER TABLE automations_v2
    ADD CONSTRAINT automations_v2_run_kind_check
    CHECK (run_kind IN ('agent', 'review'));

CREATE UNIQUE INDEX IF NOT EXISTS automations_v2_review_service_uq
    ON automations_v2 (service_id) WHERE run_kind = 'review';

ALTER TABLE automation_scm_triggers
    ADD COLUMN IF NOT EXISTS include_drafts BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS review_result JSONB;

ALTER TABLE provider_configs
    ADD COLUMN IF NOT EXISTS app_slug TEXT NOT NULL DEFAULT '';
