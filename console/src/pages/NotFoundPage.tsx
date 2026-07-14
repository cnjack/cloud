import { ArrowLeft } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { ActionLink } from '../components/PageLayout';
import styles from './NotFoundPage.module.css';

export function NotFoundPage() {
  const { t } = useTranslation();
  return (
    <section className={styles.stage}>
      <div className={styles.copy}>
        <span className={styles.code}>ROUTE / 404</span>
        <h1>{t('notFound.title')}</h1>
        <p>{t('notFound.body')}</p>
        <div><ActionLink to="/projects" variant="primary"><ArrowLeft size={14} aria-hidden="true" />{t('notFound.backToProjects')}</ActionLink></div>
      </div>
      <div className={styles.path} aria-hidden="true" />
    </section>
  );
}
