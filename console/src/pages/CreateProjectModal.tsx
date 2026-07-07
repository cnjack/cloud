/*
 * CreateProjectModal — J1-S2/S3 & J2-S1. Fields: name, git repo URL, default
 * branch (defaults to `main`), and the git-integration section (F3: readonly
 * diff | draft PR + provider repo/url). name + repo URL required. On success
 * closes and navigates to the new project's run list.
 */
import { useState } from 'react';
import { Modal } from '../components/Modal';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import {
  GitIntegrationFields,
  emptyGitIntegration,
  gitIntegrationPayload,
  validateGitIntegration,
  type GitIntegrationState,
} from '../components/GitIntegrationFields';
import { useCreateProject } from '../api/queries';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import type { Project } from '../api/types';

export function CreateProjectModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (project: Project) => void;
}) {
  const create = useCreateProject();
  const toast = useToast();

  const [name, setName] = useState('');
  const [repoUrl, setRepoUrl] = useState('');
  const [branch, setBranch] = useState('main');
  const [git, setGit] = useState<GitIntegrationState>(emptyGitIntegration);
  const [errors, setErrors] = useState<{
    name?: string;
    repo?: string;
    providerRepo?: string;
  }>({});

  const reset = () => {
    setName('');
    setRepoUrl('');
    setBranch('main');
    setGit(emptyGitIntegration);
    setErrors({});
  };

  const close = () => {
    if (create.isPending) return;
    reset();
    onClose();
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const nextErrors: typeof errors = {};
    if (!name.trim()) nextErrors.name = 'Name is required.';
    if (!repoUrl.trim()) nextErrors.repo = 'Repository URL is required.';
    Object.assign(nextErrors, validateGitIntegration(git));
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;

    create.mutate(
      {
        name: name.trim(),
        repo_url: repoUrl.trim(),
        default_branch: branch.trim() || 'main',
        ...gitIntegrationPayload(git),
      },
      {
        onSuccess: (project) => {
          toast.push({ kind: 'success', message: `Project “${project.name}” created.` });
          reset();
          onCreated(project);
        },
        onError: (err) => {
          const msg =
            err instanceof ApiError ? err.message : 'Failed to create project.';
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
      <form id="create-project-form" onSubmit={submit} noValidate>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 'var(--space-4)' }}>
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
            label="Git repository URL"
            required
            placeholder="https://gitea.local/acme/demo.git"
            value={repoUrl}
            onChange={(e) => setRepoUrl(e.target.value)}
            error={errors.repo}
            hint="Cloned into an ephemeral workspace in your cluster. Private repos need a configured provider token."
            data-testid="project-repo-input"
            autoComplete="off"
          />
          <TextField
            label="Default branch"
            placeholder="main"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            data-testid="project-branch-input"
            autoComplete="off"
          />
          <GitIntegrationFields
            value={git}
            onChange={setGit}
            errors={{ providerRepo: errors.providerRepo }}
          />
        </div>
      </form>
    </Modal>
  );
}
