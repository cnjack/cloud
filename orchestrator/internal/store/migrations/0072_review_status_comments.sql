-- 0072_review_status_comments: one durable, provider-side status comment per
-- Service pull request. The row is an outbox/state projection: desired fields
-- describe what should be visible remotely, while applied fields record the
-- last provider-confirmed representation.

CREATE TABLE IF NOT EXISTS review_status_comments (
    service_id        TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    provider          TEXT NOT NULL CHECK (provider IN ('github','gitlab','gitea')),
    provider_repo_id  TEXT NOT NULL CHECK (provider_repo_id <> ''),
    repository_path   TEXT NOT NULL CHECK (repository_path <> ''),
    pr_number         BIGINT NOT NULL CHECK (pr_number > 0),
    current_run_id    TEXT REFERENCES runs(id) ON DELETE SET NULL,
    head_sha          TEXT NOT NULL DEFAULT '',
    accepted_sequence BIGINT NOT NULL CHECK (accepted_sequence > 0),
    comment_id        TEXT NOT NULL DEFAULT '',
    comment_url       TEXT NOT NULL DEFAULT '',
    desired_state     TEXT NOT NULL CHECK (desired_state IN (
        'queued','running','publishing','completed','failed','canceled','superseded'
    )),
    applied_state     TEXT NOT NULL DEFAULT '' CHECK (applied_state = '' OR applied_state IN (
        'queued','running','publishing','completed','failed','canceled','superseded'
    )),
    desired_body_hash TEXT NOT NULL,
    applied_body_hash TEXT NOT NULL DEFAULT '',
    claim_token       TEXT NOT NULL DEFAULT '',
    claimed_at        TIMESTAMPTZ,
    attempts          INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error        TEXT NOT NULL DEFAULT '',
    next_attempt_at   TIMESTAMPTZ,
    observed_run_status TEXT NOT NULL DEFAULT '',
    observed_run_phase TEXT NOT NULL DEFAULT '',
    observed_failure_reason TEXT NOT NULL DEFAULT '',
    observed_delivery_status TEXT NOT NULL DEFAULT '',
    observed_review_posted BOOLEAN NOT NULL DEFAULT FALSE,
    observed_review_plan_hash TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (service_id, provider, provider_repo_id, pr_number),
    CHECK ((claim_token = '' AND claimed_at IS NULL)
        OR (claim_token <> '' AND claimed_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS review_status_comments_pending_idx
    ON review_status_comments ((COALESCE(next_attempt_at, updated_at)), service_id, provider, provider_repo_id, pr_number)
    WHERE current_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS review_status_comments_current_run_idx
    ON review_status_comments (current_run_id)
    WHERE current_run_id IS NOT NULL;

-- The receipt cursor is deliberately separate from the comment outbox. A
-- current Provider observation must fence an older handler even when the newer
-- event is ignored (for example because the PR is now a draft) and therefore
-- never creates a status comment or Run.
CREATE TABLE IF NOT EXISTS review_status_cursors (
    service_id        TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    provider          TEXT NOT NULL CHECK (provider IN ('github','gitlab','gitea')),
    provider_repo_id  TEXT NOT NULL CHECK (provider_repo_id <> ''),
    pr_number         BIGINT NOT NULL CHECK (pr_number > 0),
    head_sha          TEXT NOT NULL CHECK (head_sha <> ''),
    accepted_sequence BIGINT NOT NULL CHECK (accepted_sequence > 0),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (service_id, provider, provider_repo_id, pr_number)
);

-- A webhook process can die after it records "received" but before it creates
-- the durable Run. Lease/fencing fields let a redelivery safely resume that
-- normalized event after a bounded stale window. Event-key uniqueness keeps
-- the replay idempotent if the original process committed the Run first.
ALTER TABLE webhook_receipts
    ADD COLUMN IF NOT EXISTS claim_token TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;

CREATE SEQUENCE IF NOT EXISTS webhook_receipt_ingress_sequence_seq;

ALTER TABLE webhook_receipts
	ADD COLUMN IF NOT EXISTS ingress_sequence BIGINT;

WITH sequence_base AS (
    SELECT GREATEST(
        COALESCE(MAX(ingress_sequence), 0),
        (SELECT last_value FROM webhook_receipt_ingress_sequence_seq)
    ) AS value
    FROM webhook_receipts
), ordered_receipts AS (
    SELECT id, ROW_NUMBER() OVER (ORDER BY received_at, id) AS offset
    FROM webhook_receipts
    WHERE ingress_sequence IS NULL
)
UPDATE webhook_receipts AS receipt
SET ingress_sequence = sequence_base.value + ordered_receipts.offset
FROM sequence_base, ordered_receipts
WHERE receipt.id = ordered_receipts.id;

SELECT setval(
	'webhook_receipt_ingress_sequence_seq',
	GREATEST(
		COALESCE((SELECT MAX(ingress_sequence) FROM webhook_receipts), 0),
		(SELECT last_value FROM webhook_receipt_ingress_sequence_seq),
		1
	),
	TRUE
);

ALTER TABLE webhook_receipts
	ALTER COLUMN ingress_sequence SET DEFAULT nextval('webhook_receipt_ingress_sequence_seq'),
	ALTER COLUMN ingress_sequence SET NOT NULL;

ALTER SEQUENCE webhook_receipt_ingress_sequence_seq
	OWNED BY webhook_receipts.ingress_sequence;

CREATE UNIQUE INDEX IF NOT EXISTS webhook_receipts_ingress_sequence_uq
    ON webhook_receipts (ingress_sequence);

CREATE INDEX IF NOT EXISTS webhook_receipts_stale_claim_idx
    ON webhook_receipts (claimed_at, provider, delivery_id)
    WHERE status = 'received';
