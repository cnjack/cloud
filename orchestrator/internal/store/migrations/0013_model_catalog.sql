-- 0013_model_catalog: multi-row model catalog + project authorization (D21).
--
-- The single-row cluster_model_config (0010) becomes a CATALOG of models a
-- cluster admin registers (model_configs), each grantable to individual projects
-- (model_grants). A service may pin a default model (services.default_model_id)
-- and every run records the model it was actually dispatched with
-- (runs.model_id, audit). The effective model for a run is now resolved PER
-- PROJECT (see internal/modelcfg) instead of a single global row; the D16 reverse
-- proxy is unchanged (the real key still never enters a pod).
--
-- Migration (§6): the existing single cluster_model_config row (if any) becomes
-- the catalog's first entry and is GRANTED to every existing project, so existing
-- deployments keep running seamlessly. The old table is then dropped. Idempotent:
-- creates/columns use IF NOT EXISTS and the data-migration + drop are guarded by
-- to_regclass so re-applying the file is a no-op.

-- The model catalog. id/created_at mirror the app's TEXT ids (domain.NewID).
-- api_key_enc holds the AES-256-GCM ciphertext (NULL/empty = keyless endpoint);
-- the plaintext key is NEVER read back over the API.
CREATE TABLE IF NOT EXISTS model_configs (
    id          TEXT         PRIMARY KEY,
    name        TEXT         NOT NULL UNIQUE,
    base_url    TEXT         NOT NULL,
    model_name  TEXT         NOT NULL,
    api_key_enc BYTEA,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_by  TEXT         NOT NULL DEFAULT ''
);

-- model ↔ project authorization. A granted project may USE the model (read-only:
-- it never sees the base_url/key detail, only id/name/model_name). Cascades on
-- both sides so deleting a model or a project cleans up its grants.
CREATE TABLE IF NOT EXISTS model_grants (
    model_id   TEXT NOT NULL REFERENCES model_configs(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id)      ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (model_id, project_id)
);
CREATE INDEX IF NOT EXISTS model_grants_project_idx ON model_grants (project_id);

-- A service may pin a default model; a run records the model it was dispatched
-- with. Both ON DELETE SET NULL so removing a model never blocks the delete —
-- service defaults fall back to the resolution chain and historical runs simply
-- lose the (now-deleted) reference.
ALTER TABLE services ADD COLUMN IF NOT EXISTS default_model_id TEXT
    REFERENCES model_configs(id) ON DELETE SET NULL;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS model_id TEXT
    REFERENCES model_configs(id) ON DELETE SET NULL;
-- model_name is a plain-text SNAPSHOT of the provider/model id chosen at dispatch.
-- It is NOT an FK, so it survives ON DELETE SET NULL of model_id: after a model is
-- deleted a run's model_id is NULL but model_name still records what it ran on
-- (audit), keeping the run traceable.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS model_name TEXT NOT NULL DEFAULT '';

-- Data migration: fold the legacy single-row config into the catalog and grant
-- it to every existing project, then drop the old table. Guarded so re-applying
-- the file (after the table is gone) is a clean no-op.
DO $$
BEGIN
    IF to_regclass('cluster_model_config') IS NOT NULL THEN
        WITH migrated AS (
            INSERT INTO model_configs (id, name, base_url, model_name, api_key_enc, updated_by)
            SELECT md5(random()::text || clock_timestamp()::text),
                   COALESCE(NULLIF(model_name, ''), 'default'),
                   base_url, model_name, api_key_enc, updated_by
            FROM cluster_model_config
            WHERE id = 1
            ON CONFLICT (name) DO NOTHING
            RETURNING id
        )
        INSERT INTO model_grants (model_id, project_id)
        SELECT m.id, p.id FROM migrated m CROSS JOIN projects p
        ON CONFLICT DO NOTHING;

        DROP TABLE cluster_model_config;
    END IF;
END $$;
