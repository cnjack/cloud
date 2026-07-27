ALTER TABLE automations_v2
    ADD COLUMN IF NOT EXISTS model_id TEXT REFERENCES model_configs(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS model_effort TEXT NOT NULL DEFAULT '';

ALTER TABLE automations_v2
    DROP CONSTRAINT IF EXISTS automations_v2_model_effort_check;
ALTER TABLE automations_v2
    ADD CONSTRAINT automations_v2_model_effort_check
    CHECK (model_effort IN ('', 'low', 'medium', 'high'));
