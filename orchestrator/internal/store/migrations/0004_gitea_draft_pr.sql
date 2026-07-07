-- 0004_gitea_draft_pr: per-project git integration config + per-run draft-PR
-- state, for stretch goal ST-1 (Gitea draft-PR closed loop; see docs/10-prd.md
-- §3.3, docs/02-decision-log.md D08/D09).
--
-- Projects gain a git integration mode. The default is 'readonly' — today's
-- behavior, where a run ends in a diff artifact only and NOTHING is pushed. When
-- set to 'draft_pr', a successful run with a non-empty diff pushes an
-- agent/run-<id> branch and the orchestrator opens a draft PR on the configured
-- provider (Gitea for the MVP). NEW columns are non-breaking: existing rows get
-- the readonly default, so J1-J3 behavior is unchanged.
--
-- Runs gain the draft-PR result columns. git_branch/commit_sha are reported by
-- the runner (run.git event) once it has pushed; pr_url/pr_number are stamped by
-- the reconciler once it has opened (or found) the draft PR. All default empty /
-- NULL, so a readonly run never populates them.
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS git_mode      TEXT NOT NULL DEFAULT 'readonly',
    ADD COLUMN IF NOT EXISTS provider      TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS provider_url  TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS provider_repo TEXT NOT NULL DEFAULT '';

ALTER TABLE runs
    -- Branch the runner pushed (agent/run-<id>) and the commit it points at,
    -- reported via the run.git event. git_branch is the idempotency key the
    -- reconciler uses to look up an existing PR before creating one.
    ADD COLUMN IF NOT EXISTS git_branch TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS commit_sha TEXT NOT NULL DEFAULT '',
    -- Draft PR the orchestrator opened. pr_number is 0 when no PR exists.
    ADD COLUMN IF NOT EXISTS pr_url    TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS pr_number INT  NOT NULL DEFAULT 0;
