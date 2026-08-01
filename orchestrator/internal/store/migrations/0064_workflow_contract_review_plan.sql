-- 0064: immutable workflow execution contract and deterministic review input.
-- Existing rows remain NULL and are rendered as explicit legacy runs.
ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS execution_contract JSONB,
    ADD COLUMN IF NOT EXISTS review_plan JSONB,
    ADD COLUMN IF NOT EXISTS pr_head_sha TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS pr_base_sha TEXT NOT NULL DEFAULT '';

-- Repository and acting-principal facts are frozen with the same dispatch
-- snapshot as provider configuration and credential versions.
ALTER TABLE run_plugin_snapshots
    ADD COLUMN IF NOT EXISTS repository_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS repository_path TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS clone_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS default_branch TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS acting_principal_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS acting_principal_id TEXT NOT NULL DEFAULT '';
