import { ArrowLeft, GitBranch, Info, Lightning, Plus, Robot } from '@phosphor-icons/react';
import { useState } from 'react';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { useCreateProject } from '../api/queries';
import { ApiError } from '../api/client';
import { Button } from '../components/Button';
import { TextField } from '../components/Field';
import { useToast } from '../components/Toast';
import styles from './NewProjectPage.module.css';

export function NewProjectPage() {
  const { t } = useTranslation();
  const [name, setName] = useState('');
  const [error, setError] = useState<string>();
  const create = useCreateProject();
  const navigate = useNavigate();
  const toast = useToast();

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setError(t('projects.new.nameRequired'));
      return;
    }
    setError(undefined);
    create.mutate({ name: trimmed }, {
      onSuccess: (project) => {
        toast.push({ kind: 'success', message: t('projects.new.created') });
        navigate(`/projects/${project.id}`);
      },
      onError: (cause) => setError(cause instanceof ApiError ? cause.message : t('projects.new.createFailed')),
    });
  };

  return (
    <div className={styles.page}>
      <header className={styles.editorHeader}>
        <Link className={styles.back} to="/projects"><ArrowLeft size={14} aria-hidden="true" />{t('projects.title')}</Link>
        <div className={styles.editorTitle}><h1>{t('projects.new.heading')}</h1><p>{t('projects.new.subheading')}</p></div>
        <span className={styles.draft}>{t('projects.new.draft')}</span>
      </header>
      <form className={styles.grid} onSubmit={submit} noValidate>
        <div className={styles.main}>
          <section className={styles.formSection}>
            <div className={styles.sectionCopy}><span className={styles.eyebrow}>{t('projects.new.step1Eyebrow')}</span><h2>{t('projects.new.section1Title')}</h2><p>{t('projects.new.section1Body')}</p></div>
            <div className={styles.fields}>
              <TextField
                label={t('projects.new.nameLabel')}
                required
                autoFocus
                autoComplete="off"
                placeholder={t('projects.new.namePlaceholder')}
                value={name}
                onChange={(event) => { setName(event.target.value); if (error) setError(undefined); }}
                error={error}
                hint={t('projects.new.nameHint')}
              />
            </div>
          </section>
          <section className={styles.formSection}>
            <div className={styles.sectionCopy}><span className={styles.eyebrow}>{t('projects.new.step2Eyebrow')}</span><h2>{t('projects.new.section2Title')}</h2><p>{t('projects.new.section2Body')}</p></div>
            <div className={styles.callout}><Info size={16} aria-hidden="true" /><span><strong>{t('projects.new.calloutStrong')}</strong>{t('projects.new.calloutBody')}</span></div>
          </section>
          <div className={styles.actions}>
            <Link className={styles.cancel} to="/projects">{t('common.cancel')}</Link>
            <Button type="submit" variant="primary" loading={create.isPending}><Plus size={14} aria-hidden="true" />{t('projects.new.submit')}</Button>
          </div>
        </div>
        <aside className={styles.aside}>
          <span className={styles.eyebrow}>{t('projects.new.asideEyebrow')}</span>
          <h2>{t('projects.new.asideTitle')}</h2>
          <p>{t('projects.new.asideBody')}</p>
          <ul>
            <li><GitBranch size={16} aria-hidden="true" /><span>{t('projects.new.asideItem1')}</span></li>
            <li><Robot size={16} aria-hidden="true" /><span>{t('projects.new.asideItem2')}</span></li>
            <li><Lightning size={16} aria-hidden="true" /><span>{t('projects.new.asideItem3')}</span></li>
          </ul>
        </aside>
      </form>
    </div>
  );
}
