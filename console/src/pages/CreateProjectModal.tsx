/*
 * CreateProjectModal — the new-project form (multitenant blueprint §5).
 * A project is a pure container: it is created with just a name, then
 * repositories are attached on the project page ("+ Add repository"), each with
 * its own git mode. onCreated navigates there so the natural next step is in
 * front of the user.
 */
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
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
  const { t } = useTranslation();
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
      setError(t('projects.create.nameRequired'));
      return;
    }
    setError(undefined);

    create.mutate(
      { name: name.trim() },
      {
        onSuccess: (project) => {
          toast.push({ kind: 'success', message: t('projects.create.created', { name: project.name }) });
          reset();
          onCreated(project);
        },
        onError: (err) => {
          const msg = err instanceof ApiError ? err.message : t('projects.create.createFailed');
          toast.push({ kind: 'error', message: msg });
        },
      },
    );
  };

  return (
    <Modal
      open={open}
      onClose={close}
      title={t('projects.create.modalTitle')}
      data-testid="create-project-modal"
      footer={
        <>
          <Button variant="ghost" onClick={close} type="button">
            {t('common.cancel')}
          </Button>
          <Button
            variant="primary"
            type="submit"
            form="create-project-form"
            loading={create.isPending}
            data-testid="create-project-submit"
          >
            {t('projects.create.submit')}
          </Button>
        </>
      }
    >
      <form id="create-project-form" onSubmit={submit} noValidate className={styles.form}>
        <TextField
          label={t('projects.create.nameLabel')}
          required
          placeholder={t('projects.create.namePlaceholder')}
          value={name}
          onChange={(e) => setName(e.target.value)}
          error={error}
          hint={t('projects.create.nameHint')}
          data-testid="project-name-input"
          autoComplete="off"
        />
      </form>
    </Modal>
  );
}
