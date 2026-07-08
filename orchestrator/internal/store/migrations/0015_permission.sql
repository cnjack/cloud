-- 0015_permission: interactive permission approval for session runs (F8b, the
-- orchestrator half of D22's permission-approval design; the runner half F8a is
-- runner/acpdrive/permission.go).
--
-- A session run dispatched with permission_mode='approval' forwards every jcode
-- permission request to the control plane as an agent.permission_request event;
-- the user answers via POST /api/v1/runs/{id}/permission-response and the runner
-- long-polls GET /internal/v1/runs/{id}/permissions/{request_id}/decision.
--
-- run_permissions is the durable ledger of those requests. Three field groups:
--   identity   — request_id (acpdrive-generated uuid, globally unique → PK),
--                run_id, tool_call_id, title, options (the JSONB array of
--                {option_id,name,kind} offered by jcode, echoed verbatim).
--   decided_*  — the USER's answer (permission-response endpoint): which option,
--                by whom, when. Written once; a second answer is 409'd.
--   resolved_* — the RUNNER's final word (agent.permission_resolved event):
--                which option actually took effect and whether it was the user's
--                decision ('user') or a timeout-deny ('timeout'). resolved_option_id
--                may be '' (timeout with no reject-kind option → ACP Cancelled).
--
-- decided ≠ resolved on purpose: a user decision that arrives after the runner's
-- client-side timeout is recorded (decided_*) but never took effect (resolution
-- 'timeout') — the ledger keeps both sides auditable.

CREATE TABLE IF NOT EXISTS run_permissions (
    request_id          TEXT        PRIMARY KEY,
    run_id              TEXT        NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    tool_call_id        TEXT        NOT NULL DEFAULT '',
    title               TEXT        NOT NULL DEFAULT '',
    options             JSONB       NOT NULL DEFAULT '[]',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_option_id   TEXT,
    decided_by          TEXT        REFERENCES users(id) ON DELETE SET NULL,
    decided_at          TIMESTAMPTZ,
    resolved_option_id  TEXT,
    resolution          TEXT,
    resolved_at         TIMESTAMPTZ
);

-- The console/API reads a run's requests together; the decision long-poll hits
-- the PK directly.
CREATE INDEX IF NOT EXISTS run_permissions_run_idx ON run_permissions (run_id, created_at);

-- runs.permission_mode: '' = full_access (today's behaviour, the default for
-- every existing and non-session run); 'approval' = forward permission requests
-- for interactive approval (only valid on session runs — enforced at the API).
ALTER TABLE runs ADD COLUMN IF NOT EXISTS permission_mode TEXT NOT NULL DEFAULT '';
