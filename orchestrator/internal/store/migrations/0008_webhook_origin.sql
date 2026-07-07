-- 0008_webhook_origin: M7 @mention webhook — Gitea PR comments trigger runs.
--
-- A run may now be triggered by a Gitea PR comment (`@jcode …`) rather than the
-- API/console. Three additive columns record that provenance and give us a
-- durable de-dup key so a redelivered webhook cannot create the run twice:
--
--   origin: 'api' (default) | 'webhook'. Every existing run backfills to 'api'.
--   origin_comment_id:  the Gitea comment id that triggered a webhook run — the
--     idempotency key. NULL for api-origin runs.
--   origin_comment_url: the comment's html_url, surfaced as the run-header
--     "from PR comment ↗" chip (blueprint §8 UI).
--
-- The partial UNIQUE index makes a redelivered webhook (same comment id) a
-- no-op at the storage layer too, independent of the handler's pre-check. It is
-- partial (WHERE origin_comment_id IS NOT NULL) so the many api-origin runs with
-- a NULL comment id do not collide.
--
-- All additive, defaulted, and safe on a populated DB.

ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS origin             TEXT NOT NULL DEFAULT 'api'
        CHECK (origin IN ('api','webhook')),
    ADD COLUMN IF NOT EXISTS origin_comment_id  TEXT,
    ADD COLUMN IF NOT EXISTS origin_comment_url TEXT;

-- De-dup: at most one run per triggering webhook comment.
CREATE UNIQUE INDEX IF NOT EXISTS runs_origin_comment_id_uq
    ON runs (origin_comment_id)
    WHERE origin_comment_id IS NOT NULL;

-- Scan for succeeded webhook agent runs whose bundle was pushed onto an existing
-- PR head branch but whose push has not completed yet (commit_sha unset). The
-- mode/provider gate is applied by the reconciler after joining the service.
CREATE INDEX IF NOT EXISTS runs_update_push_pending_idx
    ON runs (status)
    WHERE origin = 'webhook' AND kind = 'agent'
      AND git_branch <> '' AND pr_url <> '' AND commit_sha = '';
