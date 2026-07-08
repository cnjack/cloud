-- 0012_run_result: first-class run OUTCOME column (D18).
--
-- A run that completes cleanly but produces an EMPTY diff (the agent decided no
-- code change was needed) is a SUCCESS, not a failure. Before D18 the runner
-- exited non-zero on an empty diff and the run showed failed(agent_error) —
-- misleading. Now the runner reports run.result{outcome:no_changes}, exits 0,
-- and the reconciler marks the run succeeded from the Job state as usual; this
-- column records the first-class outcome so the console/kanban writeback can
-- render "no changes" instead of a bare success.
--
-- Nullable with NO default: NULL means "ordinary run (produced a diff) / no
-- outcome reported". The only value written today is 'no_changes'; left as an
-- open TEXT for forward-compatible outcomes. Additive and safe on a populated DB.
ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS result TEXT;
