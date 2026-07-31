ALTER TABLE automation_cron_triggers
    ADD COLUMN IF NOT EXISTS output_mode TEXT NOT NULL DEFAULT 'run_only';

ALTER TABLE automation_cron_triggers
    DROP CONSTRAINT IF EXISTS automation_cron_triggers_output_mode_check;
ALTER TABLE automation_cron_triggers
    ADD CONSTRAINT automation_cron_triggers_output_mode_check
    CHECK (output_mode IN ('run_only','create_card'));

CREATE TABLE IF NOT EXISTS automation_executions (
    id                  TEXT PRIMARY KEY,
    automation_id       TEXT NOT NULL,
    automation_name     TEXT NOT NULL,
    prompt_snapshot     TEXT NOT NULL,
    project_id          TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    service_id          TEXT NOT NULL,
    trigger_kind        TEXT NOT NULL CHECK (trigger_kind IN ('scm','cron','manual')),
    event_key           TEXT NOT NULL,
    state               TEXT NOT NULL CHECK (state IN (
                            'accepted','ignored','duplicate','superseded',
                            'blocked','queued','running','terminal')),
    outcome             TEXT NOT NULL DEFAULT '',
    output_mode         TEXT NOT NULL CHECK (output_mode IN ('run_only','create_card')),
    reason_code         TEXT NOT NULL DEFAULT '',
    reason_message      TEXT NOT NULL DEFAULT '',
    repair_role         TEXT NOT NULL DEFAULT '',
    requested_actor     JSONB NOT NULL DEFAULT '{}'::jsonb,
    accountable_actor   JSONB NOT NULL DEFAULT '{}'::jsonb,
    run_id              TEXT NOT NULL DEFAULT '',
    external_url        TEXT NOT NULL DEFAULT '',
    card_automation_id  TEXT NOT NULL DEFAULT '',
    card_workspace_id   TEXT NOT NULL DEFAULT '',
    card_document_id    TEXT NOT NULL DEFAULT '',
    card_path           TEXT NOT NULL DEFAULT '',
    card_state          TEXT NOT NULL DEFAULT '',
    writeback_state     TEXT NOT NULL DEFAULT '',
    writeback_error     TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL,
    terminal_at         TIMESTAMPTZ,
    UNIQUE (automation_id, event_key)
);

CREATE INDEX IF NOT EXISTS automation_executions_history_idx
    ON automation_executions (automation_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS automation_executions_card_pending_idx
    ON automation_executions (card_state, updated_at)
    WHERE output_mode = 'create_card' AND card_state IN ('planned','creating');
