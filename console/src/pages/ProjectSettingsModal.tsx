/*
 * ProjectSettingsModal — F4. Edits a project's default branch + git integration
 * (mode / provider / provider URL / provider repo) via PATCH, and offers a
 * Delete project action behind a confirm step (DELETE). On delete the caller
 * navigates back to the projects list and shows a toast.
 *
 * The PATCH only sends fields the operator actually changed relative to the
 * loaded project, matching the orchestrator's "only provided fields" semantics
 * (11-api.md §2.1). Flipping to readonly always sends git_mode so the switch is
 * honoured; draft_pr additionally sends provider/repo (+ optional url).
 */
import { useState } from 'react';
import { Modal } from '../components/Modal';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import {
  GitIntegrationFields,
  gitIntegrationPayload,
  validateGitIntegration,
  type GitIntegrationState,
} from '../components/GitIntegrationFields';
import { useUpdateProject, useDeleteProject } from '../api/queries';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import type { GitMode, Project, UpdateProjectInput } from '../api/types';
import styles from './ProjectSettingsModal.module.css';

function toGitState(p: Project): GitIntegrationState {
  return {
    gitMode: (p.git_mode as GitMode) ?? 'readonly',
    providerUrl: p.provider_url ?? '',
    providerRepo: p.provider_repo ?? '',
  };
}

export function ProjectSettingsModal({
  open,
  project,
  onClose,
  onDeleted,
}: {
  open: boolean;
  project: Project;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const update = useUpdateProject();
  const del = useDeleteProject();
  const toast = useToast();

  const [branch, setBranch] = useState(project.default_branch);
  const [git, setGit] = useState<GitIntegrationState>(() => toGitState(project));
  const [errors, setErrors] = useState<{ providerRepo?: string }>({});
  const [confirmDelete, setConfirmDelete] = useState(false);

  const busy = update.isPending || del.isPending;

  const reset = () => {
    setBranch(project.default_branch);
    setGit(toGitState(project));
    setErrors({});
    setConfirmDelete(false);
  };

  const close = () => {
    if (busy) return;
    reset();
    onClose();
  };

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    const nextErrors = validateGitIntegration(git);
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) return;

    // Build a minimal PATCH: default_branch only if changed, plus the git
    // integration payload (git_mode always carried so a mode switch applies).
    const input: UpdateProjectInput = { ...gitIntegrationPayload(git) };
    if (branch.trim() && branch.trim() !== project.default_branch) {
      input.default_branch = branch.trim();
    }

    update.mutate(
      { id: project.id, input },
      {
        onSuccess: (updated) => {
          toast.push({ kind: 'success', message: `Project “${updated.name}” updated.` });
          onClose();
        },
        onError: (err) => {
          const msg =
            err instanceof ApiError ? err.message : 'Failed to update project.';
          toast.push({ kind: 'error', message: msg });
        },
      },
    );
  };

  const remove = () => {
    del.mutate(project.id, {
      onSuccess: () => {
        toast.push({ kind: 'success', message: `Project “${project.name}” deleted.` });
        onDeleted();
      },
      onError: (err) => {
        const msg =
          err instanceof ApiError ? err.message : 'Failed to delete project.';
        toast.push({ kind: 'error', message: msg });
      },
    });
  };

  return (
    <Modal
      open={open}
      onClose={close}
      title="Project settings"
      data-testid="project-settings-modal"
      footer={
        <>
          <Button variant="ghost" onClick={close} type="button">
            Cancel
          </Button>
          <Button
            variant="primary"
            type="submit"
            form="project-settings-form"
            loading={update.isPending}
            data-testid="project-settings-save"
          >
            Save changes
          </Button>
        </>
      }
    >
      <form id="project-settings-form" onSubmit={save} noValidate>
        <div className={styles.body}>
          <TextField
            label="Repository"
            value={project.repo_url}
            readOnly
            hint="The repository URL is fixed for the project's history."
            className={styles.repoField}
            data-testid="settings-repo"
          />
          <TextField
            label="Default branch"
            placeholder="main"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            data-testid="settings-branch-input"
            autoComplete="off"
          />
          <GitIntegrationFields
            value={git}
            onChange={setGit}
            errors={errors}
          />

          <section className={styles.danger} data-testid="danger-zone">
            <div className={styles.dangerText}>
              <span className={styles.dangerTitle}>Delete project</span>
              <span className={styles.dangerHint}>
                Permanently removes this project and all of its runs, events and
                artifacts. This cannot be undone.
              </span>
            </div>
            {confirmDelete ? (
              <div className={styles.confirmRow} data-testid="delete-confirm">
                <span className={styles.confirmLabel}>Delete for good?</span>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => setConfirmDelete(false)}
                  disabled={del.isPending}
                >
                  Keep
                </Button>
                <Button
                  type="button"
                  variant="danger"
                  size="sm"
                  loading={del.isPending}
                  onClick={remove}
                  data-testid="project-delete-confirm"
                >
                  Delete project
                </Button>
              </div>
            ) : (
              <Button
                type="button"
                variant="danger"
                size="sm"
                onClick={() => setConfirmDelete(true)}
                disabled={busy}
                data-testid="project-delete"
              >
                Delete project
              </Button>
            )}
          </section>
        </div>
      </form>
    </Modal>
  );
}
