/*
 * PrPanel — the Run page "PR" tab (blueprint §5). Shows the pull request link +
 * live state badge, a "Request AI review" button (member+; the backend is the
 * final arbiter — a viewer never reaches this because the button is hidden), and
 * the list of AI review runs targeting the PR with their markdown output.
 *
 * Kept deliberately simple: one link row, one primary action, one list.
 */
import { useState } from 'react';
import { ArrowSquareOut } from '@phosphor-icons/react';
import { useNavigate } from 'react-router-dom';
import { usePR, useRequestReview } from '../api/queries';
import { ApiError } from '../api/client';
import type { PrState, ReviewRunSummary } from '../api/types';
import { Button } from './Button';
import { useModelGate } from './ModelGate';
import { StatusBadge } from './StatusBadge';
import { Markdown } from './Markdown';
import { LoadingBlock, ErrorBlock } from './States';
import { useToast } from './Toast';
import { timeAgo } from '../lib/format';
import styles from './PrPanel.module.css';

const PR_STATE_LABELS: Record<PrState, string> = {
  open: 'Open',
  merged: 'Merged',
  closed: 'Closed',
  unknown: 'Unknown',
};

function normalizeState(state: string): PrState {
  return state === 'open' || state === 'merged' || state === 'closed'
    ? state
    : 'unknown';
}

/** Small pill badge for the PR's provider state (reuses the StatusBadge pill
 *  language; colour comes from status tokens via data-state). */
export function PrStateBadge({ state }: { state: string }) {
  const s = normalizeState(state);
  return (
    <span
      className={styles.stateBadge}
      data-state={s}
      role="status"
      aria-label={`Pull request state: ${PR_STATE_LABELS[s]}`}
    >
      {PR_STATE_LABELS[s]}
    </span>
  );
}

export function PrPanel({
  runId,
  projectId,
  canReview,
}: {
  runId: string;
  projectId: string;
  canReview: boolean;
}) {
  const pr = usePR(runId, true);
  const navigate = useNavigate();
  const toast = useToast();
  const requestReview = useRequestReview();
  // Fail-visible (D21): a review run invokes the LLM too, so the button gets the
  // same disabled+notice treatment as the composer (the 409 backstops).
  const modelGate = useModelGate(projectId, canReview);

  const doReview = () =>
    requestReview.mutate(runId, {
      onSuccess: (run) => {
        toast.push({ kind: 'success', message: 'AI review requested.' });
        navigate(`/runs/${run.id}`);
      },
      onError: (err) =>
        toast.push({
          kind: 'error',
          message: err instanceof ApiError ? err.message : 'Could not request a review.',
        }),
    });

  if (pr.isLoading && !pr.data) return <LoadingBlock label="Loading pull request…" />;
  if (pr.isError && !pr.data)
    return (
      <ErrorBlock
        error={pr.error}
        onRetry={() => pr.refetch()}
        title="Couldn't load the pull request"
      />
    );

  const info = pr.data!;

  return (
    <div className={styles.wrap} data-testid="pr-panel">
      <div className={styles.linkRow}>
        {info.url ? (
          <a
            className={styles.prLink}
            href={info.url}
            target="_blank"
            rel="noreferrer"
            data-testid="pr-external-link"
          >
            {info.url}
            <ArrowSquareOut className={styles.arrow} size={14} weight="regular" aria-hidden="true" />
          </a>
        ) : (
          <span className={styles.noPr}>No pull request opened yet.</span>
        )}
        <PrStateBadge state={info.state} />
        {info.head_branch && <span className={styles.branch}>{info.head_branch}</span>}
      </div>

      {canReview && (
        <div className={styles.actionRow}>
          <Button
            variant="primary"
            onClick={doReview}
            loading={requestReview.isPending}
            disabled={!modelGate.configured}
            data-testid="request-review-btn"
          >
            Request AI review
          </Button>
          {modelGate.notice}
        </div>
      )}

      <section className={styles.reviews}>
        <h3 className={styles.reviewsTitle}>Reviews</h3>
        {info.review_runs.length === 0 ? (
          <p className={styles.empty} data-testid="reviews-empty">
            No AI reviews yet.
          </p>
        ) : (
          <div className={styles.reviewList}>
            {info.review_runs.map((rr) => (
              <ReviewItem key={rr.id} review={rr} />
            ))}
          </div>
        )}
      </section>
    </div>
  );
}

function ReviewItem({ review }: { review: ReviewRunSummary }) {
  const hasOutput = !!review.review_output;
  const [open, setOpen] = useState(true);
  return (
    <article className={styles.review} data-testid="review-item">
      <div className={styles.reviewHead}>
        <StatusBadge status={review.status} size="sm" />
        <span className={styles.reviewMeta}>
          {timeAgo(review.created_at)}
          {review.triggered_by_display_name
            ? ` · ${review.triggered_by_display_name}`
            : ''}
        </span>
        {hasOutput && (
          <button
            type="button"
            className={styles.toggle}
            onClick={() => setOpen((v) => !v)}
            data-testid="review-toggle"
          >
            {open ? 'Hide' : 'Show'}
          </button>
        )}
      </div>
      {hasOutput ? (
        open && (
          <div className={styles.reviewBody}>
            <Markdown source={review.review_output} />
          </div>
        )
      ) : (
        <p className={styles.pending}>
          {review.status === 'failed'
            ? 'The review run failed before producing output.'
            : 'Review in progress…'}
        </p>
      )}
    </article>
  );
}
