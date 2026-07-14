import { ArrowLeft, GitBranch, Info, Lightning, Plus, Robot } from '@phosphor-icons/react';
import { useState } from 'react';
import type { FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useCreateProject } from '../api/queries';
import { ApiError } from '../api/client';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { useToast } from '../components/Toast';
import styles from './NewProjectPage.module.css';

export function NewProjectPage() {
  const [name, setName] = useState('');
  const [error, setError] = useState<string>();
  const create = useCreateProject();
  const navigate = useNavigate();
  const toast = useToast();

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setError('Name is required.');
      return;
    }
    setError(undefined);
    create.mutate({ name: trimmed }, {
      onSuccess: (project) => {
        toast.push({ kind: 'success', message: 'Project created.' });
        navigate(`/projects/${project.id}`);
      },
      onError: (cause) => setError(cause instanceof ApiError ? cause.message : 'Could not create the Project.'),
    });
  };

  return (
    <div className={styles.page}>
      <header className={styles.editorHeader}>
        <Link className={styles.back} to="/projects"><ArrowLeft size={14} aria-hidden="true" />Projects</Link>
        <div className={styles.editorTitle}><h1>Create a Project</h1><p>Start with the ownership boundary. Services come next.</p></div>
        <span className={styles.draft}>draft</span>
      </header>
      <form className={styles.grid} onSubmit={submit} noValidate>
        <div className={styles.main}>
          <section className={styles.formSection}>
            <div className={styles.sectionCopy}><span className={styles.eyebrow}>01 / identity</span><h2>Project details</h2><p>Use the product or team boundary people will recognize.</p></div>
            <div className={styles.fields}>
              <TextField
                label="Name"
                required
                autoFocus
                autoComplete="off"
                placeholder="e.g. jcode Cloud"
                value={name}
                onChange={(event) => { setName(event.target.value); if (error) setError(undefined); }}
                error={error}
                hint="You can rename the Project later without changing Service URLs."
              />
            </div>
          </section>
          <section className={styles.formSection}>
            <div className={styles.sectionCopy}><span className={styles.eyebrow}>02 / boundary</span><h2>What belongs here</h2><p>Repositories are added as Services after creation.</p></div>
            <div className={styles.callout}><Info size={16} aria-hidden="true" /><span><strong>No repository is created or connected yet.</strong>The resulting workspace opens empty, where the owner explicitly authorizes each Service.</span></div>
          </section>
          <div className={styles.actions}>
            <Link className={styles.cancel} to="/projects">Cancel</Link>
            <Button type="submit" variant="primary" loading={create.isPending}><Plus size={14} aria-hidden="true" />Create Project</Button>
          </div>
        </div>
        <aside className={styles.aside}>
          <span className={styles.eyebrow}>After creation</span>
          <h2>The workspace opens empty.</h2>
          <p>This keeps Project creation honest: a container can exist even when no Git provider or model is available yet.</p>
          <ul>
            <li><GitBranch size={16} aria-hidden="true" /><span>Connect repositories as Services.</span></li>
            <li><Robot size={16} aria-hidden="true" /><span>Review models granted by the Cluster administrator.</span></li>
            <li><Lightning size={16} aria-hidden="true" /><span>Start a Task only after required dependencies are configured.</span></li>
          </ul>
        </aside>
      </form>
    </div>
  );
}
