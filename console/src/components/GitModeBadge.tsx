/*
 * GitModeBadge — F3b. A compact indicator of a project's git integration shown
 * next to the repo line on the project detail page.
 *   - readonly  → "Read-only diff" (neutral hairline chip)
 *   - draft_pr  → "Draft PR → owner/name" (violet accent; reads as the
 *                 reviewable-PR promise, mono provider_repo)
 * Absent git_mode is treated as readonly (older projects / diff-only default).
 */
import type { GitMode } from '../api/types';
import styles from './GitModeBadge.module.css';

export function GitModeBadge({
  gitMode,
  providerRepo,
}: {
  gitMode?: GitMode | string;
  providerRepo?: string;
}) {
  const isDraftPr = gitMode === 'draft_pr';

  if (isDraftPr) {
    return (
      <span
        className={styles.badge}
        data-mode="draft_pr"
        data-testid="git-mode-badge"
        title="Successful runs push a branch and open a draft PR to review."
      >
        <span className={styles.dot} aria-hidden />
        Draft PR
        {providerRepo && (
          <>
            <span className={styles.arrow} aria-hidden>
              →
            </span>
            <code className={styles.repo}>{providerRepo}</code>
          </>
        )}
      </span>
    );
  }

  return (
    <span
      className={styles.badge}
      data-mode="readonly"
      data-testid="git-mode-badge"
      title="Successful runs produce a diff artifact only — nothing is pushed."
    >
      <span className={styles.dot} aria-hidden />
      Read-only diff
    </span>
  );
}
