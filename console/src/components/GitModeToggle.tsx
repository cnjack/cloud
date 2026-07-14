/*
 * GitModeToggle — the segmented Read-only diff | Draft PR control shared by the
 * new-project and project-settings forms (multitenant blueprint §5). The repo is
 * a single smart-parsed URL now, so there are no separate provider fields: the
 * server derives the provider/owner-name from the URL and rejects draft_pr on a
 * raw repo (git://, no owner/name path) with a clean 400.
 */
import { useTranslation } from 'react-i18next';
import type { GitMode } from '../api/types';
import styles from './GitModeToggle.module.css';

const MODES: { value: GitMode; labelKey: string; hintKey: string }[] = [
  {
    value: 'readonly',
    labelKey: 'components.gitMode.readonlyLabel',
    hintKey: 'components.gitMode.readonlyHint',
  },
  {
    value: 'draft_pr',
    labelKey: 'components.gitMode.draftPrLabel',
    hintKey: 'components.gitMode.draftPrHint',
  },
];

export function GitModeToggle({
  value,
  onChange,
}: {
  value: GitMode;
  onChange: (next: GitMode) => void;
}) {
  const { t } = useTranslation();
  const activeMode = MODES.find((m) => m.value === value);
  const activeHint = activeMode ? t(activeMode.hintKey) : undefined;
  return (
    <div className={styles.wrap}>
      <span className={styles.legend}>{t('components.gitMode.legend')}</span>
      <div
        className={styles.segmented}
        role="radiogroup"
        aria-label={t('components.gitMode.modeAria')}
        data-testid="git-mode-control"
      >
        {MODES.map((m) => (
          <button
            key={m.value}
            type="button"
            role="radio"
            aria-checked={value === m.value}
            className={styles.segment}
            data-active={value === m.value || undefined}
            data-testid={`git-mode-${m.value}`}
            onClick={() => onChange(m.value)}
          >
            {t(m.labelKey)}
          </button>
        ))}
      </div>
      {activeHint && <p className={styles.modeHint}>{activeHint}</p>}
    </div>
  );
}
