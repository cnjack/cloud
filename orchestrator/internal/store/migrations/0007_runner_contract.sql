-- 0007_runner_contract: M3 runner-contract inversion + review channel schema.
--
-- The runner no longer pushes: a draft_pr agent run uploads a git BUNDLE and the
-- orchestrator pushes the branch + opens the draft PR on the triggering user's
-- behalf. Two storage needs follow:
--
--   1. run_artifacts.content_bytes — the diff artifact stays text (content), but
--      the bundle is BINARY (a git bundle contains NUL bytes), which a TEXT
--      column cannot hold. Add a nullable BYTEA column; kind='bundle' rows carry
--      their payload here (content stays '').
--
--   2. runs review + PR-association columns —
--      review_posted_at: idempotency marker stamped once a review run's output
--        has been posted as a PR review comment (reconcile review pass).
--      pr_head_branch / pr_base_branch: associate a review run (kind='review')
--        with the PR it reviews. The runner diffs base...head; the reconcile pass
--        finds the target PR by its head branch. Empty for agent runs.
--
-- All additive, defaulted, and safe on a populated DB.

ALTER TABLE run_artifacts
    ADD COLUMN IF NOT EXISTS content_bytes BYTEA;

ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS review_posted_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS pr_head_branch   TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS pr_base_branch   TEXT NOT NULL DEFAULT '';

-- Scan for succeeded review runs awaiting a posted comment (reconcile review pass).
CREATE INDEX IF NOT EXISTS runs_review_pending_idx
    ON runs (status)
    WHERE kind = 'review' AND review_posted_at IS NULL;
