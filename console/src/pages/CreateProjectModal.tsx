/*
 * CreateProjectModal — the new-project form (multitenant blueprint §5).
 * A project is a pure container: it is created with just a name, then
 * repositories are attached on the project page ("+ Add repository"), each with
 * its own git mode. onCreated navigates there so the natural next step is in
 * front of the user.
 */
import { useState } from 'react';
import { Modal } from '../components/Modal';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { useCreateProject } from '../api/queries';
import { useToast } from '../components/Toast';
import { ApiError } from '../api/client';
import type { Project } from '../api/types';
import styles from './CreateProjectModal.module.css';

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
  const [error, setError] = useState<string>();

  const reset = () => {
    setName('');
    setError(undefined);
  };

  const close = () => {
    if (create.isPending) return;
    reset();
    onClose();
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setError('Name is required.');
      return;
    }
    setError(undefined);

    create.mutate(
      { name: name.trim() },
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
          error={error}
          hint="You'll add repositories on the project page next."
          data-testid="project-name-input"
          autoComplete="off"
        />
      </form>
    </Modal>
  );
}
