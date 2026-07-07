/*
 * ProjectSettingsModal — owner/cluster-admin project settings (blueprint §2/§5).
 * Two tabs:
 *   - General: project rename + a Delete-project action behind a confirm step.
 *     Repo config (branch / git mode) lives on the project page per repository —
 *     a project is a pure container now.
 *   - Members: roster with role management + add-by-search (MembersPanel).
 */
import { useState } from 'react';
import { Modal } from '../components/Modal';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { MembersPanel } from './MembersPanel';
import { useUpdateProject, useDeleteProject } from '../api/queries';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import type { Project } from '../api/types';
import styles from './ProjectSettingsModal.module.css';

type Tab = 'general' | 'members';

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

  // Absent role (demo / legacy) is treated as owner (full affordances).
  const canManage = (project.role ?? 'owner') === 'owner';

  const [tab, setTab] = useState<Tab>('general');
  const [name, setName] = useState(project.name);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const busy = update.isPending || del.isPending;

  const reset = () => {
    setName(project.name);
    setConfirmDelete(false);
    setTab('general');
  };

  const close = () => {
    if (busy) return;
    reset();
    onClose();
  };

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    const next = name.trim();
    if (!next || next === project.name) {
      onClose();
      return;
    }
    update.mutate(
      { id: project.id, input: { name: next } },
      {
        onSuccess: (updated) => {
          toast.push({ kind: 'success', message: `Project “${updated.name}” updated.` });
          onClose();
        },
        onError: (err) => {
          const msg = err instanceof ApiError ? err.message : 'Failed to update project.';
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
        const msg = err instanceof ApiError ? err.message : 'Failed to delete project.';
        toast.push({ kind: 'error', message: msg });
      },
    });
  };

  const footer =
    tab === 'general' ? (
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
    ) : (
      <Button variant="secondary" onClick={close} type="button" data-testid="members-done">
        Done
      </Button>
    );

  return (
    <Modal
      open={open}
      onClose={close}
      title="Project settings"
      data-testid="project-settings-modal"
      footer={footer}
    >
      <div className={styles.tabs} role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'general'}
          className={styles.tab}
          data-active={tab === 'general' || undefined}
          onClick={() => setTab('general')}
          data-testid="tab-general"
        >
          General
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'members'}
          className={styles.tab}
          data-active={tab === 'members' || undefined}
          onClick={() => setTab('members')}
          data-testid="tab-members"
        >
          Members
        </button>
      </div>

      {tab === 'general' ? (
        <form id="project-settings-form" onSubmit={save} noValidate>
          <div className={styles.body}>
            <TextField
              label="Name"
              placeholder="demo"
              value={name}
              onChange={(e) => setName(e.target.value)}
              hint="Repository settings (branch, git mode) live on each repository on the project page."
              data-testid="settings-name-input"
              autoComplete="off"
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
      ) : (
        <MembersPanel projectId={project.id} canManage={canManage} />
      )}
    </Modal>
  );
}
