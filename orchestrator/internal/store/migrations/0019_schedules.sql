-- 0019_schedules: service-level cron trigger (F11 / D24 前半).
--
-- Vision (docs/14 §1.4): a `schedules` row binds a standard 5-field cron
-- expression + a prompt template to a service. A single poller tick (mirroring
-- the D17 kanban poller's poll/idempotency philosophy) scans enabled schedules
-- each interval; when a schedule's next fire is due it dispatches a headless
-- agent run (origin='schedule') against the service, with the service's default
-- model (the F4/D21 resolution chain), and advances last_fired_at.
--
-- Idempotency / anti-double-dispatch: the poller advances last_fired_at with a
-- CONDITIONAL update (WHERE last_fired_at IS NOT DISTINCT FROM $old) so two
-- orchestrator instances cannot both fire a single window — exactly one wins the
-- CAS and dispatches. On restart the window is advanced to the CURRENT time, not
-- backfilled: missed windows are dropped, never replayed as a burst of runs.
--
-- Fail-visible (CLAUDE.md red line #1): last_error records WHY a due window was
-- abandoned without dispatching (LLM not configured, integration host no longer
-- cluster-allowed). The window is still advanced (so it is not retried forever)
-- and the API/console surface the reason; a later successful dispatch clears it.
--
-- The triggering schedule id is NOT a column on runs — it is recorded on the
-- dispatched run's initial run.status event (schedule_id) so the runs table stays
-- unchanged (docs/14 §5.7).
--
-- Idempotent: CREATE TABLE / INDEX IF NOT EXISTS so a re-apply is a no-op.

CREATE TABLE IF NOT EXISTS schedules (
    id             TEXT        PRIMARY KEY,
    service_id     TEXT        NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    cron_expr      TEXT        NOT NULL,
    prompt         TEXT        NOT NULL,
    enabled        BOOLEAN     NOT NULL DEFAULT TRUE,
    last_fired_at  TIMESTAMPTZ,
    last_error     TEXT        NOT NULL DEFAULT '',
    created_by     TEXT        REFERENCES users(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fast scans: the poller reads only enabled schedules; the API lists per service.
CREATE INDEX IF NOT EXISTS schedules_enabled_idx
    ON schedules (enabled)
    WHERE enabled = TRUE;
CREATE INDEX IF NOT EXISTS schedules_service_idx ON schedules (service_id);

-- Widen the runs.origin CHECK (0011 set it to ('api','webhook','kanban')) to also
-- accept 'schedule'. DROP CONSTRAINT IF EXISTS + re-add keeps this idempotent.
ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_origin_check;
ALTER TABLE runs
    ADD CONSTRAINT runs_origin_check
    CHECK (origin IN ('api','webhook','kanban','schedule'));
