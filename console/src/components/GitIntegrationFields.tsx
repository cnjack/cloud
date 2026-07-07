/*
 * GitIntegrationFields — the shared "Git integration" section used by both the
 * create-project modal (F3) and the project settings modal (F4). Renders a
 * segmented control for git_mode (Read-only diff | Draft PR) and, when Draft PR
 * is selected, reveals the provider / provider URL / provider repo fields with
 * helper text + inline validation.
 *
 * State is lifted to the caller so both modals can submit the exact payload the
 * orchestrator expects (11-api.md §2.1). validateGitIntegration() mirrors the
 * orchestrator's draft_pr rules (provider_repo required, owner/name shape).
 */
import { TextField } from './Field';
import type { GitMode } from '../api/types';
import styles from './GitIntegrationFields.module.css';

export interface GitIntegrationState {
  gitMode: GitMode;
  providerUrl: string;
  providerRepo: string;
}

export const emptyGitIntegration: GitIntegrationState = {
  gitMode: 'readonly',
  providerUrl: '',
  providerRepo: '',
};

/** owner/name — one slash, non-empty segments, no spaces. */
const PROVIDER_REPO_RE = /^[^/\s]+\/[^/\s]+$/;

export interface GitIntegrationErrors {
  providerRepo?: string;
}

/** Contract-aligned validation (11-api.md §2.1): draft_pr needs owner/name. */
export function validateGitIntegration(
  state: GitIntegrationState,
): GitIntegrationErrors {
  const errors: GitIntegrationErrors = {};
  if (state.gitMode === 'draft_pr') {
    const repo = state.providerRepo.trim();
    if (!repo) {
      errors.providerRepo = 'Provider repository is required for Draft PR.';
    } else if (!PROVIDER_REPO_RE.test(repo)) {
      errors.providerRepo = 'Use owner/name, e.g. jcloud/seed.';
    }
  }
  return errors;
}

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

export function GitIntegrationFields({
  value,
  onChange,
  errors,
}: {
  value: GitIntegrationState;
  onChange: (next: GitIntegrationState) => void;
  errors?: GitIntegrationErrors;
}) {
  const set = (patch: Partial<GitIntegrationState>) =>
    onChange({ ...value, ...patch });
  const activeHint = MODES.find((m) => m.value === value.gitMode)?.hint;

  return (
    <fieldset className={styles.fieldset}>
      <legend className={styles.legend}>Git integration</legend>

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
            aria-checked={value.gitMode === m.value}
            className={styles.segment}
            data-active={value.gitMode === m.value || undefined}
            data-testid={`git-mode-${m.value}`}
            onClick={() => set({ gitMode: m.value })}
          >
            {m.label}
          </button>
        ))}
      </div>
      {activeHint && <p className={styles.modeHint}>{activeHint}</p>}

      {value.gitMode === 'draft_pr' && (
        <div className={styles.draftFields} data-testid="draft-pr-fields">
          <TextField
            label="Provider"
            value="Gitea"
            readOnly
            hint="Gitea is the only supported provider in this release."
            data-testid="provider-input"
          />
          <TextField
            label="Provider repository"
            required
            placeholder="owner/name"
            value={value.providerRepo}
            onChange={(e) => set({ providerRepo: e.target.value })}
            error={errors?.providerRepo}
            hint="owner/name on the provider, e.g. jcloud/seed."
            data-testid="provider-repo-input"
            autoComplete="off"
          />
          <TextField
            label="Provider URL"
            placeholder="http://gitea.jcloud.svc.cluster.local:3000"
            value={value.providerUrl}
            onChange={(e) => set({ providerUrl: e.target.value })}
            hint="Optional — defaults to the orchestrator's configured Gitea URL."
            data-testid="provider-url-input"
            autoComplete="off"
          />
        </div>
      )}
    </fieldset>
  );
}

/**
 * Build the create/patch payload fields from the git-integration state, matching
 * the orchestrator contract: draft_pr sends provider=gitea + repo (+ optional
 * url); readonly sends only git_mode so a PATCH can flip a project back.
 */
export function gitIntegrationPayload(state: GitIntegrationState): {
  git_mode: GitMode;
  provider?: 'gitea';
  provider_url?: string;
  provider_repo?: string;
} {
  if (state.gitMode === 'draft_pr') {
    const payload: {
      git_mode: GitMode;
      provider: 'gitea';
      provider_url?: string;
      provider_repo: string;
    } = {
      git_mode: 'draft_pr',
      provider: 'gitea',
      provider_repo: state.providerRepo.trim(),
    };
    const url = state.providerUrl.trim();
    if (url) payload.provider_url = url;
    return payload;
  }
  return { git_mode: 'readonly' };
}
