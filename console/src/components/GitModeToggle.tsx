/*
 * GitModeToggle — the segmented Read-only diff | Draft PR control shared by the
 * new-project and project-settings forms (multitenant blueprint §5). The repo is
 * a single smart-parsed URL now, so there are no separate provider fields: the
 * server derives the provider/owner-name from the URL and rejects draft_pr on a
 * raw repo (git://, no owner/name path) with a clean 400.
 */
import type { GitMode } from '../api/types';
import styles from './GitModeToggle.module.css';

const MODES: { value: GitMode; label: string; hint: string }[] = [
  {
    value: 'readonly',
    label: 'Read-only diff',
    hint: 'A successful run produces a diff artifact only — nothing is pushed.',
  },
  {
    value: 'draft_pr',
    label: 'Draft PR',
    hint: 'On success the agent pushes a branch and opens a draft PR to review. Never auto-merges, never triggers CI.',
  },
];

export function GitModeToggle({
  value,
  onChange,
}: {
  value: GitMode;
  onChange: (next: GitMode) => void;
}) {
  const activeHint = MODES.find((m) => m.value === value)?.hint;
  return (
    <div className={styles.wrap}>
      <span className={styles.legend}>Git integration</span>
      <div
        className={styles.segmented}
        role="radiogroup"
        aria-label="Git integration mode"
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
            {m.label}
          </button>
        ))}
      </div>
      {activeHint && <p className={styles.modeHint}>{activeHint}</p>}
    </div>
  );
}
