-- 0073_review_completion_status: preserve an explicit terminal partial state
-- for provider-side review status comments. A partial review may publish
-- confirmed findings, but it must never converge to the completed/clean state.

ALTER TABLE review_status_comments
    DROP CONSTRAINT IF EXISTS review_status_comments_desired_state_check;

ALTER TABLE review_status_comments
    ADD CONSTRAINT review_status_comments_desired_state_check CHECK (desired_state IN (
        'queued','running','publishing','completed','partial','failed','canceled','superseded'
    ));

ALTER TABLE review_status_comments
    DROP CONSTRAINT IF EXISTS review_status_comments_applied_state_check;

ALTER TABLE review_status_comments
    ADD CONSTRAINT review_status_comments_applied_state_check CHECK (applied_state = '' OR applied_state IN (
        'queued','running','publishing','completed','partial','failed','canceled','superseded'
    ));

-- Re-project already-published legacy results that have no trustworthy
-- completion receipt. Native reviews are immutable, but the mutable status
-- comment must stop presenting those attempts as clean/completed.
UPDATE review_status_comments AS comment
SET observed_review_posted = FALSE,
    updated_at = now()
FROM runs AS review_run
WHERE comment.current_run_id = review_run.id
  AND comment.applied_state = 'completed'
  AND (
      review_run.review_result IS NULL
      OR COALESCE(review_run.review_result->'completion'->>'status', '') <> 'complete'
  );
