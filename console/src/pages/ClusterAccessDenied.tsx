import { ArrowLeft, Lock, Warning } from '@phosphor-icons/react';
import { ActionLink, SurfaceInner } from '../components/PageLayout';
import styles from './ClusterAccessDenied.module.css';

export function ClusterAccessDenied() {
  return (
    <SurfaceInner>
      <section className={styles.stage} data-testid="cluster-access-denied">
        <div className={styles.copy}>
          <span className={styles.code}>ROLE / PROJECT ADMIN</span>
          <h1>Cluster stays outside this role.</h1>
          <p>Your role manages Projects only. Capacity, model credentials, guardrails, and provider configuration require a Cluster administrator.</p>
          <div className={styles.notice}><Warning size={16} aria-hidden="true" /><span><strong>The Cluster navigation is intentionally hidden.</strong>This direct route explains the boundary without issuing a system snapshot request.</span></div>
          <div><ActionLink to="/projects" variant="primary"><ArrowLeft size={14} aria-hidden="true" />Back to Projects</ActionLink></div>
        </div>
        <div className={styles.boundary} aria-hidden="true"><Lock size={24} /></div>
      </section>
    </SurfaceInner>
  );
}
