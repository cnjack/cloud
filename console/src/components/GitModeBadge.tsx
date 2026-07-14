/*
 * GitModeBadge — F3b. A compact indicator of a project's git integration shown
 * next to the repo line on the project detail page.
 *   - readonly  → "Read-only diff" (neutral hairline chip)
 *   - draft_pr  → "Draft PR → owner/name" (violet accent; reads as the
 *                 reviewable-PR promise, mono provider_repo)
 * Absent git_mode is treated as readonly (older projects / diff-only default).
 */
import { useTranslation } from 'react-i18next';
import type { GitMode } from '../api/types';
import { ArrowRight } from '@phosphor-icons/react';
import styles from './GitModeBadge.module.css';

export function GitModeBadge({
  gitMode,
  providerRepo,
}: {
  gitMode?: GitMode | string;
  providerRepo?: string;
}) {
  const { t } = useTranslation();
  const isDraftPr = gitMode === 'draft_pr';

  if (isDraftPr) {
    return (
      <span
        className={styles.badge}
        data-mode="draft_pr"
        data-testid="git-mode-badge"
        title={t('components.gitMode.draftPrTitle')}
      >
        <span className={styles.dot} aria-hidden />
        {t('components.gitMode.draftPrLabel')}
        {providerRepo && (
          <>
            <ArrowRight className={styles.arrow} size={14} weight="regular" aria-hidden="true" />
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
      title={t('components.gitMode.readonlyTitle')}
    >
      <span className={styles.dot} aria-hidden />
      {t('components.gitMode.readonlyLabel')}
    </span>
  );
}
