-- 0003_job_cleaned_at: preserve k8s_job_name as historical record.
--
-- BEFORE: the reconciler's terminal-Job cleanup path blanked k8s_job_name once
-- the Job was confirmed deleted, so terminal runs lost their Job identity —
-- breaking the audit/e2e contract that a run's Job name is part of its
-- historical record (11-api.md Run schema; e2e J3-S6 verifies independent
-- worker Jobs by name).
--
-- AFTER: cleanup bookkeeping moves to a dedicated marker. job_cleaned_at is
-- stamped (now()) when the reconciler has confirmed the run's Job is deleted;
-- k8s_job_name is never cleared. The cleanup scan selects terminal runs with a
-- job name and job_cleaned_at IS NULL, so reaped runs are not re-processed.
ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS job_cleaned_at TIMESTAMPTZ;
