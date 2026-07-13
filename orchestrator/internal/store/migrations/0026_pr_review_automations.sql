-- 0026_pr_review_automations: persistent Gitea PR-event review policies and
-- inspectable service webhook state.

CREATE TABLE IF NOT EXISTS automations (
    id                TEXT PRIMARY KEY,
    service_id        TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    instructions      TEXT NOT NULL,
    trigger_type      TEXT NOT NULL CHECK (trigger_type IN ('pr_review')),
    model_id          TEXT NOT NULL REFERENCES model_configs(id) ON DELETE RESTRICT,
    events            TEXT[] NOT NULL,
    base_branch       TEXT NOT NULL,
    include_drafts    BOOLEAN NOT NULL DEFAULT FALSE,
    enabled           BOOLEAN NOT NULL DEFAULT TRUE,
    last_triggered_at TIMESTAMPTZ,
    last_run_id       TEXT,
    last_error        TEXT NOT NULL DEFAULT '',
    created_by        TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS automations_service_idx
    ON automations (service_id, created_at DESC);

CREATE TABLE IF NOT EXISTS webhook_bindings (
    service_id          TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    provider            TEXT NOT NULL CHECK (provider IN ('gitea','github','gitlab')),
    endpoint            TEXT NOT NULL,
    status              TEXT NOT NULL CHECK (status IN ('pending','active','error')),
    last_synced_at      TIMESTAMPTZ,
    last_delivery_at    TIMESTAMPTZ,
    last_delivery_status TEXT NOT NULL DEFAULT '',
    last_error          TEXT NOT NULL DEFAULT '',
    updated_at          TIMESTAMPTZ NOT NULL
);

ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS origin_automation_id TEXT REFERENCES automations(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS origin_event_key TEXT;

ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_origin_check;
ALTER TABLE runs ADD CONSTRAINT runs_origin_check
    CHECK (origin IN ('api','webhook','kanban','schedule','automation'));

CREATE UNIQUE INDEX IF NOT EXISTS runs_origin_event_key_uq
    ON runs (origin_event_key)
    WHERE origin_event_key IS NOT NULL;
