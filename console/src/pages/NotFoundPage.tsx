import { ArrowLeft } from '@phosphor-icons/react';
import { ActionLink } from '../components/PageLayout';
import styles from './NotFoundPage.module.css';

export function NotFoundPage() {
  return (
    <section className={styles.stage}>
      <div className={styles.copy}>
        <span className={styles.code}>ROUTE / 404</span>
        <h1>This path ends here.</h1>
        <p>The page may have moved, or the link may describe a Project or Task you can no longer access.</p>
        <div><ActionLink to="/projects" variant="primary"><ArrowLeft size={14} aria-hidden="true" />Back to Projects</ActionLink></div>
      </div>
      <div className={styles.path} aria-hidden="true" />
    </section>
  );
}
