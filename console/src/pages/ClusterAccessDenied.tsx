import { ArrowLeft, Lock, Warning } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { ActionLink, SurfaceInner } from '../components/PageLayout';
import styles from './ClusterAccessDenied.module.css';

export function ClusterAccessDenied() {
  const { t } = useTranslation();
  return (
    <SurfaceInner>
      <section className={styles.stage} data-testid="cluster-access-denied">
        <div className={styles.copy}>
          <span className={styles.code}>{t('cluster.accessDenied.roleCode')}</span>
          <h1>{t('cluster.accessDenied.title')}</h1>
          <p>{t('cluster.accessDenied.body')}</p>
          <div className={styles.notice}><Warning size={16} aria-hidden="true" /><span><strong>{t('cluster.accessDenied.noticeTitle')}</strong>{t('cluster.accessDenied.noticeBody')}</span></div>
          <div><ActionLink to="/projects" variant="primary"><ArrowLeft size={14} aria-hidden="true" />{t('cluster.accessDenied.backToProjects')}</ActionLink></div>
        </div>
        <div className={styles.boundary} aria-hidden="true"><Lock size={24} /></div>
      </section>
    </SurfaceInner>
  );
}
