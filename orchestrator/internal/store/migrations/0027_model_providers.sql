-- 0027_model_providers: provider-owned model catalog.
--
-- Credentials and endpoint identity belong to a provider, while Project grants
-- continue to target individual models. Existing flat model rows are migrated
-- losslessly into one provider per model: ids, encrypted keys, model ids, service
-- defaults, run audit references, and grants remain unchanged.

CREATE TABLE IF NOT EXISTS model_providers (
    id                      TEXT        PRIMARY KEY,
    name                    TEXT        NOT NULL UNIQUE,
    kind                    TEXT        NOT NULL DEFAULT 'custom',
    base_url                TEXT        NOT NULL,
    auth_type               TEXT        NOT NULL DEFAULT 'none'
                                        CHECK (auth_type IN ('api_key', 'service_identity', 'none')),
    api_key_enc             BYTEA,
    catalog_mode            TEXT        NOT NULL DEFAULT 'auto'
                                        CHECK (catalog_mode IN ('auto', 'disabled')),
    catalog_available       BOOLEAN,
    last_verified_at        TIMESTAMPTZ,
    last_verification_error TEXT        NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by              TEXT        NOT NULL DEFAULT ''
);

ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS provider_id TEXT;
ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS context_window INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS supports_reasoning BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS supports_tools BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS supports_image BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE model_configs ADD COLUMN IF NOT EXISTS model_source TEXT NOT NULL DEFAULT 'custom';

-- Each legacy model becomes a provider with the same id. Reusing the id makes
-- this data migration deterministic and safe to re-run without comparing
-- randomized ciphertext or accidentally merging providers that used different
-- credentials against the same base URL.
INSERT INTO model_providers (
    id, name, kind, base_url, auth_type, api_key_enc, catalog_mode,
    created_at, updated_at, updated_by
)
SELECT mc.id, mc.name, 'custom', mc.base_url,
       CASE WHEN length(mc.api_key_enc) > 0 THEN 'api_key' ELSE 'none' END,
       mc.api_key_enc, 'disabled', mc.created_at, mc.updated_at, mc.updated_by
FROM model_configs mc
WHERE mc.provider_id IS NULL
ON CONFLICT (id) DO NOTHING;

UPDATE model_configs SET provider_id = id WHERE provider_id IS NULL;
UPDATE model_configs
SET model_id = CASE
    WHEN position('/' IN model_name) > 0 THEN substring(model_name FROM position('/' IN model_name) + 1)
    ELSE model_name
END
WHERE model_id = '';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'model_configs_provider_fk'
    ) THEN
        ALTER TABLE model_configs
            ADD CONSTRAINT model_configs_provider_fk
            FOREIGN KEY (provider_id) REFERENCES model_providers(id) ON DELETE CASCADE;
    END IF;
END $$;

ALTER TABLE model_configs ALTER COLUMN provider_id SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'model_configs_source_check'
    ) THEN
        ALTER TABLE model_configs
            ADD CONSTRAINT model_configs_source_check
            CHECK (model_source IN ('catalog', 'custom'));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS model_configs_provider_idx ON model_configs (provider_id);
CREATE UNIQUE INDEX IF NOT EXISTS model_configs_provider_model_idx
    ON model_configs (provider_id, model_id);
