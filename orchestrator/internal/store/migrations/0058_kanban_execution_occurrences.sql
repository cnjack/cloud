-- Durable event consumption and repeatable Card-entry execution history.
-- Occurrences intentionally do not reference Automation/Service/Installation:
-- deleting a binding must not erase execution evidence or frozen writeback
-- routing. The Run reference is nullable so ordinary run retention can proceed.

ALTER TABLE automation_kanban_triggers
    ADD COLUMN IF NOT EXISTS event_cursor BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS bootstrapped_at TIMESTAMPTZ;

ALTER TABLE automation_kanban_claims
    ADD COLUMN IF NOT EXISTS last_observed_column TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS outside_trigger_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS latest_occurrence_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS external_ref_available BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE TABLE IF NOT EXISTS automation_kanban_occurrences (
    id                    TEXT PRIMARY KEY,
    automation_id         TEXT NOT NULL,
    service_id            TEXT NOT NULL DEFAULT '',
    installation_id       TEXT NOT NULL DEFAULT '',
    workspace_id          TEXT NOT NULL DEFAULT '',
    document_id           TEXT NOT NULL,
    document_path         TEXT NOT NULL DEFAULT '',
    done_column           TEXT NOT NULL DEFAULT '',
    event_key             TEXT NOT NULL,
    event_sequence        BIGINT,
    actor_display         TEXT NOT NULL DEFAULT '',
    entry_column          TEXT NOT NULL,
    state                 TEXT NOT NULL CHECK (state IN ('received','blocked','queued','running','terminal')),
    outcome               TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('','succeeded','failed','canceled')),
    reason_code           TEXT NOT NULL DEFAULT '',
    reason_message        TEXT NOT NULL DEFAULT '',
    repair_role           TEXT NOT NULL DEFAULT '' CHECK (repair_role IN ('','project_owner','cluster_admin')),
    run_id                TEXT REFERENCES runs(id) ON DELETE SET NULL,
    receipt_phase         TEXT NOT NULL DEFAULT '',
    receipt_written_at    TIMESTAMPTZ,
    writeback_state       TEXT NOT NULL DEFAULT 'pending' CHECK (writeback_state IN ('not_required','pending','complete','unavailable')),
    writeback_error       TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    terminal_at           TIMESTAMPTZ,
    UNIQUE (automation_id, event_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS automation_kanban_occurrences_run_uq
    ON automation_kanban_occurrences (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS automation_kanban_occurrences_claim_idx
    ON automation_kanban_occurrences (automation_id, document_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS automation_kanban_occurrences_card_idx
    ON automation_kanban_occurrences (service_id, workspace_id, document_path, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS automation_kanban_occurrences_retry_idx
    ON automation_kanban_occurrences (state, updated_at)
    WHERE state IN ('received','blocked');

-- Preserve the single-run behavior shipped before occurrences as a historical
-- occurrence. New code treats claim.run_id/writeback_at only as a compatibility
-- projection of the latest occurrence.
INSERT INTO automation_kanban_occurrences (
    id, automation_id, service_id, installation_id, workspace_id,
    document_id, document_path, done_column, event_key, entry_column,
    state, outcome, run_id, receipt_phase, writeback_state,
    receipt_written_at, created_at, updated_at, terminal_at
)
SELECT
    'occ_legacy_' || md5(c.automation_id || ':' || c.document_id),
    c.automation_id,
    COALESCE(a.service_id, r.service_id, ''),
    c.installation_id,
    c.workspace_id,
    c.document_id,
    c.document_path,
    c.done_column,
    'legacy:' || c.document_id,
    COALESCE(t.trigger_column, ''),
    CASE
        WHEN r.status IN ('succeeded','failed','canceled') THEN 'terminal'
        ELSE 'queued'
    END,
    CASE
        WHEN r.status IN ('succeeded','failed','canceled') THEN r.status
        ELSE ''
    END,
    c.run_id,
    CASE WHEN c.writeback_at IS NOT NULL THEN 'terminal' ELSE '' END,
    CASE WHEN c.writeback_at IS NOT NULL THEN 'complete' ELSE 'pending' END,
    c.writeback_at,
    c.created_at,
    COALESCE(c.writeback_at, c.created_at),
    CASE WHEN r.status IN ('succeeded','failed','canceled') THEN r.finished_at END
FROM automation_kanban_claims c
JOIN runs r ON r.id = c.run_id
LEFT JOIN automations_v2 a ON a.id = c.automation_id
LEFT JOIN automation_kanban_triggers t ON t.automation_id = c.automation_id
ON CONFLICT DO NOTHING;

UPDATE automation_kanban_claims c
SET latest_occurrence_id = o.id,
    updated_at = GREATEST(c.updated_at, o.updated_at)
FROM automation_kanban_occurrences o
WHERE o.automation_id = c.automation_id
  AND o.document_id = c.document_id
  AND c.latest_occurrence_id = '';
