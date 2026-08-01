-- Execution completion and external delivery are different state machines.
-- A Run may be succeeded while its PR/review/branch publication is pending or
-- failed; consumers must not infer delivery from runs.status.
ALTER TABLE runs
    ADD COLUMN IF NOT EXISTS delivery_status TEXT NOT NULL DEFAULT 'not_required',
    ADD COLUMN IF NOT EXISTS delivery_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS delivery_error TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS delivery_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS delivery_updated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;

DO $$ BEGIN
    ALTER TABLE runs ADD CONSTRAINT runs_delivery_status_check
        CHECK (delivery_status IN ('not_required','pending','delivered','failed'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE runs ADD CONSTRAINT runs_delivery_kind_check
        CHECK (delivery_kind IN ('','artifact','pull_request','branch_update','review_comment'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

UPDATE runs r
SET delivery_status = CASE
        WHEN r.kind='review' AND r.review_posted_at IS NULL THEN 'pending'
        WHEN r.origin='webhook' AND r.kind='agent' AND r.git_branch<>'' AND r.commit_sha='' THEN 'pending'
        WHEN r.kind='agent' AND s.git_mode='draft_pr' AND r.git_branch<>'' AND r.pr_url='' THEN 'pending'
        ELSE 'delivered'
    END,
    delivery_kind = CASE
        WHEN r.kind='review' THEN 'review_comment'
        WHEN r.origin='webhook' AND r.kind='agent' AND r.git_branch<>'' THEN 'branch_update'
        WHEN r.kind='agent' AND s.git_mode='draft_pr' AND r.git_branch<>'' THEN 'pull_request'
        ELSE 'artifact'
    END,
    delivery_updated_at = COALESCE(r.finished_at,r.created_at),
    delivered_at = CASE
        WHEN (r.kind='review' AND r.review_posted_at IS NULL)
          OR (r.origin='webhook' AND r.kind='agent' AND r.git_branch<>'' AND r.commit_sha='')
          OR (r.kind='agent' AND s.git_mode='draft_pr' AND r.git_branch<>'' AND r.pr_url='')
        THEN NULL ELSE COALESCE(r.finished_at,r.created_at) END
FROM services s
WHERE r.service_id=s.id AND r.status='succeeded' AND r.delivery_status='not_required';
