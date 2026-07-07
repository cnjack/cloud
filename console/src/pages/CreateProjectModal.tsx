/*
 * CreateProjectModal — the "dumb UX" new-project form (multitenant blueprint §5).
 * Two things to fill in: a name and a single repository URL. The git-mode toggle
 * (default Read-only diff) decides whether successful runs open a draft PR.
 *
 * The server smart-parses the URL to a provider (github/gitlab/self-hosted gitea)
 * or a raw clone URL. When Draft PR is chosen and the URL points at a provider
 * the current user has NOT linked, we show an inline "Link <Provider>" prompt so
 * the orchestrator can push + open the PR as that user (blueprint §3). The
 * console-token service principal is never prompted (it pushes with the global
 * token).
 */
import { useState } from 'react';
import { Modal } from '../components/Modal';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { GitModeToggle } from '../components/GitModeToggle';
import { useCreateProject } from '../api/queries';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import { providerForRepoUrl } from '../lib/repo';
import type { AuthProviderInfo, GitMode, Me, Project } from '../api/types';
import styles from './CreateProjectModal.module.css';

function providerLabel(id: string, providers: AuthProviderInfo[]): string {
  const p = providers.find((x) => x.id === id);
  if (p) return p.name;
  return id.charAt(0).toUpperCase() + id.slice(1);
}

export function CreateProjectModal({
  open,
  onClose,
  onCreated,
  me = null,
  providers = [],
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (project: Project) => void;
  /** The current principal — drives the draft-PR "link provider" prompt. */
  me?: Me | null;
  /** Configured OAuth providers — used to render the Link button + label. */
  providers?: AuthProviderInfo[];
}) {
  const create = useCreateProject();
  const toast = useToast();

  const [name, setName] = useState('');
  const [repoUrl, setRepoUrl] = useState('');
  const [gitMode, setGitMode] = useState<GitMode>('readonly');
  const [errors, setErrors] = useState<{ name?: string; repo?: string }>({});

  const reset = () => {
    setName('');
    setRepoUrl('');
    setGitMode('readonly');
    setErrors({});
  };

  const close = () => {
    if (create.isPending) return;
    reset();
    onClose();
  };

  const trimmedRepo = repoUrl.trim();
  const repoProvider = trimmedRepo ? providerForRepoUrl(trimmedRepo) : null;
  // Draft PR against a raw repo (git://, no owner/name) is impossible — surface
  // it before submit rather than as a server 400.
  const draftPrOnRaw = gitMode === 'draft_pr' && trimmedRepo !== '' && repoProvider === null;
  // Draft PR against a provider the user has not linked → prompt to link.
  const linked = new Set(me?.identities?.map((i) => i.provider) ?? []);
  const needsLink =
    gitMode === 'draft_pr' &&
    !!repoProvider &&
    !me?.is_service &&
    !linked.has(repoProvider);
  const providerConfigured = !!repoProvider && providers.some((p) => p.id === repoProvider);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const nextErrors: typeof errors = {};
    if (!name.trim()) nextErrors.name = 'Name is required.';
    if (!trimmedRepo) nextErrors.repo = 'Repository URL is required.';
    else if (draftPrOnRaw)
      nextErrors.repo =
        'Draft PR needs a provider repository URL (github.com / gitlab.com / your Gitea).';
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;

    create.mutate(
      { name: name.trim(), repo_url: trimmedRepo, git_mode: gitMode },
      {
        onSuccess: (project) => {
          toast.push({ kind: 'success', message: `Project “${project.name}” created.` });
          reset();
          onCreated(project);
        },
        onError: (err) => {
          const msg = err instanceof ApiError ? err.message : 'Failed to create project.';
          toast.push({ kind: 'error', message: msg });
        },
      },
    );
  };

  return (
    <Modal
      open={open}
      onClose={close}
      title="New project"
      data-testid="create-project-modal"
      footer={
        <>
          <Button variant="ghost" onClick={close} type="button">
            Cancel
          </Button>
          <Button
            variant="primary"
            type="submit"
            form="create-project-form"
            loading={create.isPending}
            data-testid="create-project-submit"
          >
            Create project
          </Button>
        </>
      }
    >
      <form id="create-project-form" onSubmit={submit} noValidate className={styles.form}>
        <TextField
          label="Name"
          required
          placeholder="demo"
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={errors.name}
          data-testid="project-name-input"
          autoComplete="off"
        />
        <TextField
          label="Repository"
          required
          placeholder="https://github.com/acme/demo"
          value={repoUrl}
          onChange={(e) => setRepoUrl(e.target.value)}
          error={errors.repo}
          hint="Cloned into an ephemeral workspace in your cluster. Private repos need a configured provider token."
          data-testid="project-repo-input"
          autoComplete="off"
        />
        <GitModeToggle value={gitMode} onChange={setGitMode} />

        {needsLink && (
          <div className={styles.linkPrompt} data-testid="link-prompt">
            <span className={styles.linkText}>
              Draft PRs on {providerLabel(repoProvider!, providers)} push as you — link your{' '}
              {providerLabel(repoProvider!, providers)} account so the PR is yours.
            </span>
            {providerConfigured ? (
              <a
                className={styles.linkBtn}
                href={`/auth/link/${repoProvider}`}
                data-testid="link-provider-btn"
              >
                Link {providerLabel(repoProvider!, providers)}
              </a>
            ) : (
              <span className={styles.linkNote}>
                {providerLabel(repoProvider!, providers)} sign-in isn’t configured on this
                server.
              </span>
            )}
          </div>
        )}
      </form>
    </Modal>
  );
}
