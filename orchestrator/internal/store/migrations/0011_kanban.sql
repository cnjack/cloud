-- 0011_kanban: jtype kanban trigger + writeback integration (Feature E).
--
-- Architecture vision (docs/01 §一句话架构): dragging a jtype card into a
-- trigger column dispatches an agent run; when the run finishes the result is
-- written back as a card comment (and the card optionally moved to a done
-- column). The orchestrator POLL the jtype document API (webhooks are blocked
-- by jtype's SSRF guard; the board SSE needs a `full`-scope token) — level,
-- idempotent, restart-safe, matching the existing reconciler philosophy.
--
-- Two tables:
--
--   kanban_links  — admin-configured bindings of (jtype workspace + board +
--     trigger column) -> (project + service). One cluster-wide jtype PAT (env
--     JTYPE_TOKEN) covers every link. UNIQUE(workspace_id, board_ref): one link
--     per board.
--
--   kanban_claims — idempotency / "card seen" rows. UNIQUE(link_id, document_id)
--     => a card dispatches at most once per link. run_id is NULL until dispatch
--     succeeds (so a not-yet-configured-LLM card is retained for auto-dispatch
--     the moment the admin configures the model). writeback_at gates the
--     reconciler's result-comment pass; notified_not_configured_at throttles the
--     "LLM not configured" card comment to one per card.
--
-- Also widens runs.origin to accept 'kanban' (a run dispatched by the poller).

CREATE TABLE IF NOT EXISTS kanban_links (
    id              TEXT        PRIMARY KEY,
    workspace_id    TEXT        NOT NULL,
    board_ref       TEXT        NOT NULL,
    project_id      TEXT        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    service_id      TEXT        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    trigger_column  TEXT        NOT NULL,
    done_column     TEXT,
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, board_ref)
);

CREATE TABLE IF NOT EXISTS kanban_claims (
    id                          TEXT        PRIMARY KEY,
    link_id                     TEXT        NOT NULL REFERENCES kanban_links(id) ON DELETE CASCADE,
    document_id                 TEXT        NOT NULL,
    document_path               TEXT,
    run_id                      TEXT        REFERENCES runs(id) ON DELETE SET NULL,
    notified_not_configured_at  TIMESTAMPTZ,
    writeback_at                TIMESTAMPTZ,
    claimed_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (link_id, document_id)
);

-- Fast scans: enabled links, and pending writebacks (claim has a run, not yet
-- written back). The writeback scan joins claims->runs in the query; this index
-- narrows the claim side to rows worth visiting.
CREATE INDEX IF NOT EXISTS kanban_links_enabled_idx
    ON kanban_links (enabled)
    WHERE enabled = TRUE;
CREATE INDEX IF NOT EXISTS kanban_claims_pending_writeback_idx
    ON kanban_claims (link_id)
    WHERE run_id <> '' AND writeback_at IS NULL;

-- Widens the runs.origin CHECK (added by 0008 as ('api','webhook')) to include
-- 'kanban'. DROP CONSTRAINT IF EXISTS + re-add keeps this idempotent.
ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_origin_check;
ALTER TABLE runs
    ADD CONSTRAINT runs_origin_check
    CHECK (origin IN ('api','webhook','kanban'));
