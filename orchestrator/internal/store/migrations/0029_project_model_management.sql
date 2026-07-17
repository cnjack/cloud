-- 0029_project_model_management: project-owned model providers + models (M1).
--
-- Each PROJECT may own its own model providers + models, usable by all of that
-- project's services, exactly like the sibling jcode provider manager. The
-- existing CLUSTER-level catalog (cluster-admin providers/models + model_grants)
-- is unchanged: a NULL project_id means cluster-global (today's behavior). A
-- non-NULL project_id means the row is owned by that project and cascades when
-- the project is deleted — the same project-owned pattern as integrations (0018).
--
-- A project's usable model set = its OWN enabled models UNION the cluster models
-- granted to it (see ListModelsForProject). enabled is the per-model on/off toggle
-- (jcode Switch parity) and applies to project-owned models. headers_enc holds the
-- AES-256-GCM ciphertext of an optional custom-headers JSON map (jcode advanced
-- form parity); the plaintext is NEVER read back over the API. Like base_url and
-- api_key_enc, the provider's headers_enc is SNAPSHOTTED onto each of its model
-- rows (model_configs.headers_enc) so the runtime resolver/LLM proxy can apply the
-- custom headers on outbound requests without re-reading the provider.
--
-- The global UNIQUE(name) constraints on model_providers/model_configs are relaxed
-- to a SCOPED uniqueness keyed on COALESCE(project_id,'') so the cluster and each
-- project may independently name a provider/model "OpenAI".
--
-- Idempotent: ADD COLUMN IF NOT EXISTS, guarded constraint drops, and
-- CREATE ... IF NOT EXISTS make a re-apply a clean no-op.

ALTER TABLE model_providers ADD COLUMN IF NOT EXISTS project_id TEXT
    REFERENCES projects(id) ON DELETE CASCADE;
ALTER TABLE model_providers ADD COLUMN IF NOT EXISTS headers_enc BYTEA;

ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS project_id TEXT
    REFERENCES projects(id) ON DELETE CASCADE;
ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT true;
-- Snapshot of the owning provider's custom headers (AES-256-GCM), denormalised
-- exactly like base_url/api_key_enc so the runtime resolver + LLM proxy can apply
-- them without loading the provider. NULL when the provider has no custom headers.
ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS headers_enc BYTEA;

-- Relax the global UNIQUE(name) (auto-named <table>_name_key by 0013/0027) to a
-- scope-aware unique index. COALESCE(project_id,'') puts every cluster row in the
-- '' namespace and each project in its own, so two projects (and the cluster) can
-- each name a provider/model the same thing.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'model_providers_name_key') THEN
        ALTER TABLE model_providers DROP CONSTRAINT model_providers_name_key;
    END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS model_providers_scope_name_idx
    ON model_providers (COALESCE(project_id, ''), name);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'model_configs_name_key') THEN
        ALTER TABLE model_configs DROP CONSTRAINT model_configs_name_key;
    END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS model_configs_scope_name_idx
    ON model_configs (COALESCE(project_id, ''), name);

CREATE INDEX IF NOT EXISTS model_providers_project_idx ON model_providers (project_id);
CREATE INDEX IF NOT EXISTS model_configs_project_idx ON model_configs (project_id);
