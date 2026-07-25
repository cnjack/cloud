-- 0045_run_plugin_snapshot_credentials.sql
--
-- A durable run may outlive a Provider URL/configuration change or an
-- Installation reconnect. Immutable Provider/grant version rows let the run
-- retain its launch-time issuer without copying any secret into each snapshot.
-- Every secret column remains JCLOUD_MASTER_KEY ciphertext; none is returned
-- to a task container.

CREATE TABLE IF NOT EXISTS provider_config_versions (
    provider               TEXT NOT NULL CHECK (provider IN ('github','gitlab','gitea','jtype')),
    config_revision        BIGINT NOT NULL,
    base_url               TEXT NOT NULL,
    client_id              TEXT NOT NULL DEFAULT '',
    client_secret_enc      BYTEA,
    app_id                 TEXT NOT NULL DEFAULT '',
    app_private_key_enc    BYTEA,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, config_revision)
);

-- No FK to plugin_installations: an Installation can be uninstalled while an
-- unrelated active run still needs its immutable launch snapshot for audit and
-- the short period it is allowed to finish.
CREATE TABLE IF NOT EXISTS plugin_credential_versions (
    id                    TEXT PRIMARY KEY,
    installation_id       TEXT NOT NULL,
    provider              TEXT NOT NULL CHECK (provider IN ('github','gitlab','gitea','jtype')),
    github_installation_id TEXT NOT NULL DEFAULT '',
    access_token_enc      BYTEA,
    refresh_token_enc     BYTEA,
    token_expires_at      TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, installation_id, provider)
);
CREATE INDEX IF NOT EXISTS plugin_credential_versions_installation_idx
    ON plugin_credential_versions(installation_id, created_at DESC);

ALTER TABLE plugin_installations
    ADD COLUMN IF NOT EXISTS credential_version_id TEXT NOT NULL DEFAULT '';

-- Seed immutable version rows for pre-0045 state. The legacy ids are internal
-- opaque strings; all newly-created versions use application generated ids.
INSERT INTO provider_config_versions(
    provider,config_revision,base_url,client_id,client_secret_enc,
    app_id,app_private_key_enc,created_at
)
SELECT provider,config_revision,base_url,client_id,client_secret_enc,
       app_id,app_private_key_enc,updated_at
FROM provider_configs
ON CONFLICT(provider,config_revision) DO NOTHING;

INSERT INTO plugin_credential_versions(
    id,installation_id,provider,github_installation_id,
    access_token_enc,refresh_token_enc,token_expires_at,created_at
)
SELECT 'legacy-' || id,id,provider,github_installation_id,
       access_token_enc,refresh_token_enc,token_expires_at,updated_at
FROM plugin_installations
WHERE credential_version_id = ''
ON CONFLICT(id) DO NOTHING;

UPDATE plugin_installations
SET credential_version_id = 'legacy-' || id
WHERE credential_version_id = '';

ALTER TABLE run_plugin_snapshots
    ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS provider_config_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS credential_version_id TEXT NOT NULL DEFAULT '';

-- Existing deployments may already have active snapshots when they upgrade.
-- Capture the currently associated configuration/grant once during migration.
-- If a row cannot be backfilled, it is intentionally left invalid and will be
-- skipped by the credential endpoint rather than ever targeting a new URL.
UPDATE run_plugin_snapshots s
SET provider = pi.provider,
    provider_config_revision = pi.config_revision,
    credential_version_id = pi.credential_version_id
FROM plugin_installations pi
WHERE pi.id = s.installation_id
  AND s.provider = '';

-- An obsolete row with no surviving Installation/version cannot safely be
-- issued after this migration. Remove it rather than retaining an unauditable
-- reference which could later be accidentally resolved against a new grant.
DELETE FROM run_plugin_snapshots s
WHERE NOT EXISTS (
    SELECT 1
    FROM provider_config_versions pv
    JOIN plugin_credential_versions cv
      ON cv.id = s.credential_version_id
     AND cv.installation_id = s.installation_id
     AND cv.provider = s.provider
    WHERE pv.provider = s.provider
      AND pv.config_revision = s.provider_config_revision
);

ALTER TABLE plugin_installations
    ADD CONSTRAINT plugin_installations_credential_version_fk
    FOREIGN KEY (credential_version_id, id, provider)
    REFERENCES plugin_credential_versions(id, installation_id, provider)
    DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE run_plugin_snapshots
    ADD CONSTRAINT run_plugin_snapshots_provider_version_fk
    FOREIGN KEY (provider, provider_config_revision)
    REFERENCES provider_config_versions(provider, config_revision),
    ADD CONSTRAINT run_plugin_snapshots_credential_version_fk
    FOREIGN KEY (credential_version_id, installation_id, provider)
    REFERENCES plugin_credential_versions(id, installation_id, provider);
