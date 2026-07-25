-- 0043_plugin_platform: clean-cut Project Plugin platform.
--
-- This is intentionally a release boundary: legacy integrations, board links,
-- schedules, automations, services and runs are discarded rather than migrated.
-- Projects, users, memberships, model catalogue and API keys survive. Do not
-- replace these explicit deletes with TRUNCATE ... CASCADE: that could reach
-- project/user data which is outside this clean-cut's scope.

DELETE FROM kanban_claims;
DELETE FROM kanban_links;
DELETE FROM webhook_bindings;
DELETE FROM automations;
DELETE FROM schedules;
DELETE FROM runs;
DELETE FROM services;
DELETE FROM integrations;

CREATE TABLE IF NOT EXISTS cluster_settings (
    id              SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    public_url      TEXT NOT NULL DEFAULT '',
    setup_complete  BOOLEAN NOT NULL DEFAULT FALSE,
    updated_by      TEXT REFERENCES users(id) ON DELETE SET NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS provider_configs (
    provider                TEXT PRIMARY KEY CHECK (provider IN ('github','gitlab','gitea','jtype')),
    base_url                TEXT NOT NULL DEFAULT '',
    login_enabled           BOOLEAN NOT NULL DEFAULT FALSE,
    plugin_enabled          BOOLEAN NOT NULL DEFAULT FALSE,
    client_id               TEXT NOT NULL DEFAULT '',
    client_secret_enc       BYTEA,
    app_id                  TEXT NOT NULL DEFAULT '',
    app_private_key_enc     BYTEA,
    webhook_secret_enc      BYTEA,
    capability_version      TEXT NOT NULL DEFAULT '',
    capabilities            TEXT[] NOT NULL DEFAULT '{}',
    config_revision         BIGINT NOT NULL DEFAULT 1,
    last_health_error       TEXT NOT NULL DEFAULT '',
    last_capability_check   TIMESTAMPTZ,
    updated_by              TEXT REFERENCES users(id) ON DELETE SET NULL,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS plugin_installations (
    id                       TEXT PRIMARY KEY,
    project_id               TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider                 TEXT NOT NULL CHECK (provider IN ('github','gitlab','gitea','jtype')),
    status                   TEXT NOT NULL CHECK (status IN ('connecting','enabled','disabled','action_required','uninstalling','error')),
    external_account_id      TEXT NOT NULL DEFAULT '',
    external_account         TEXT NOT NULL DEFAULT '',
    github_installation_id   TEXT NOT NULL DEFAULT '',
    workspace_id             TEXT NOT NULL DEFAULT '',
    scopes                   TEXT[] NOT NULL DEFAULT '{}',
    access_token_enc         BYTEA,
    refresh_token_enc        BYTEA,
    token_expires_at         TIMESTAMPTZ,
    consent_version          TEXT NOT NULL DEFAULT '',
    consented_by             TEXT REFERENCES users(id) ON DELETE SET NULL,
    consented_at             TIMESTAMPTZ,
    config_revision          BIGINT NOT NULL DEFAULT 1,
    last_health_error        TEXT NOT NULL DEFAULT '',
    last_healthy_at          TIMESTAMPTZ,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, provider)
);
CREATE INDEX IF NOT EXISTS plugin_installations_project_idx ON plugin_installations (project_id, created_at DESC);

CREATE TABLE IF NOT EXISTS service_repository_bindings (
    service_id       TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    installation_id  TEXT NOT NULL REFERENCES plugin_installations(id) ON DELETE RESTRICT,
    provider_repo_id TEXT NOT NULL,
    repository_path  TEXT NOT NULL,
    clone_url        TEXT NOT NULL DEFAULT '',
    default_branch   TEXT NOT NULL DEFAULT 'main',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (installation_id, provider_repo_id)
);

-- Unified automations use strongly typed child tables so cron, kanban and SCM
-- do not require an unvalidated JSON configuration blob. The legacy table was
-- explicitly cleared above and is not part of the Plugin API surface.
CREATE TABLE IF NOT EXISTS automations_v2 (
    id                 TEXT PRIMARY KEY,
    service_id         TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    installation_id    TEXT REFERENCES plugin_installations(id) ON DELETE RESTRICT,
    name               TEXT NOT NULL,
    trigger_kind       TEXT NOT NULL CHECK (trigger_kind IN ('scm','kanban','cron')),
    prompt_template    TEXT NOT NULL,
    enabled            BOOLEAN NOT NULL DEFAULT TRUE,
    ignore_jcode       BOOLEAN NOT NULL DEFAULT TRUE,
    last_triggered_at  TIMESTAMPTZ,
    last_run_id        TEXT,
    last_error         TEXT NOT NULL DEFAULT '',
    created_by         TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS automations_v2_service_idx ON automations_v2 (service_id, created_at DESC);
CREATE INDEX IF NOT EXISTS automations_v2_enabled_idx ON automations_v2 (trigger_kind, enabled) WHERE enabled = TRUE;

-- Migration 0026 tied run origins to the retired automations table. This
-- release has already deleted all runs above, so retarget the constraint before
-- any v2 Automation can dispatch a new run.
ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_origin_automation_id_fkey;
ALTER TABLE runs
    ADD CONSTRAINT runs_origin_automation_id_fkey
    FOREIGN KEY (origin_automation_id) REFERENCES automations_v2(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS automation_scm_triggers (
    automation_id TEXT PRIMARY KEY REFERENCES automations_v2(id) ON DELETE CASCADE,
    branch         TEXT NOT NULL DEFAULT '',
    path_pattern   TEXT NOT NULL DEFAULT '',
    conclusion     TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS automation_scm_actions (
    automation_id TEXT NOT NULL REFERENCES automations_v2(id) ON DELETE CASCADE,
    service_id    TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    event_family  TEXT NOT NULL,
    action        TEXT NOT NULL,
    PRIMARY KEY (automation_id, event_family, action),
    UNIQUE (service_id, event_family, action)
);

CREATE TABLE IF NOT EXISTS automation_kanban_triggers (
    automation_id  TEXT PRIMARY KEY REFERENCES automations_v2(id) ON DELETE CASCADE,
    installation_id TEXT NOT NULL REFERENCES plugin_installations(id) ON DELETE RESTRICT,
    board_ref      TEXT NOT NULL,
    trigger_column TEXT NOT NULL,
    done_column    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS automation_kanban_claims (
    automation_id TEXT NOT NULL REFERENCES automations_v2(id) ON DELETE CASCADE,
    document_id   TEXT NOT NULL,
    document_path TEXT NOT NULL DEFAULT '',
    run_id        TEXT REFERENCES runs(id) ON DELETE SET NULL,
    writeback_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (automation_id, document_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS automation_kanban_claims_run_uq
    ON automation_kanban_claims (run_id) WHERE run_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS automation_cron_triggers (
    automation_id TEXT PRIMARY KEY REFERENCES automations_v2(id) ON DELETE CASCADE,
    cron_expr     TEXT NOT NULL,
    last_fired_at TIMESTAMPTZ,
    last_error    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS webhook_receipts (
    id                    TEXT PRIMARY KEY,
    provider              TEXT NOT NULL CHECK (provider IN ('github','gitlab','gitea','jtype')),
    delivery_id           TEXT NOT NULL,
    installation_id       TEXT REFERENCES plugin_installations(id) ON DELETE SET NULL,
    event_family          TEXT NOT NULL,
    action                TEXT NOT NULL,
    external_actor_id     TEXT NOT NULL DEFAULT '',
    external_actor        TEXT NOT NULL DEFAULT '',
    object_ref            TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'received',
    matched_automation_id TEXT REFERENCES automations_v2(id) ON DELETE SET NULL,
    error                 TEXT NOT NULL DEFAULT '',
    received_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at            TIMESTAMPTZ NOT NULL DEFAULT now() + interval '30 days',
    UNIQUE (provider, delivery_id)
);
CREATE INDEX IF NOT EXISTS webhook_receipts_expiry_idx ON webhook_receipts (expires_at);

CREATE TABLE IF NOT EXISTS run_plugin_snapshots (
    run_id           TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    -- No foreign key: a snapshot is an immutable record of what a running task
    -- was allowed to use. Uninstalling later must not delete/alter this audit
    -- identity or fail because an unrelated run still has a snapshot.
    installation_id  TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, installation_id)
);

CREATE TABLE IF NOT EXISTS plugin_audit_events (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    installation_id   TEXT REFERENCES plugin_installations(id) ON DELETE SET NULL,
    actor_user_id     TEXT REFERENCES users(id) ON DELETE SET NULL,
    event_type        TEXT NOT NULL,
    detail            TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS plugin_audit_events_project_idx ON plugin_audit_events (project_id, created_at DESC);
