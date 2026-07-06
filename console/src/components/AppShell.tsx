/*
 * AppShell — top nav (wordmark + project switcher + demo tag) and the routed
 * content region. Responsive: primary ≥1024px, usable at 768px.
 */
import { useNavigate, useParams } from 'react-router-dom';
import type { ReactNode } from 'react';
import { Wordmark } from './Wordmark';
import { ProjectSwitcher } from './ProjectSwitcher';
import { useDemoMode } from '../api/ApiProvider';
import styles from './AppShell.module.css';

export function AppShell({ children }: { children: ReactNode }) {
  const demo = useDemoMode();
  const navigate = useNavigate();
  const params = useParams();
  const activeProjectId = params.projectId;

  return (
    <div className={styles.shell}>
      <header className={styles.topbar}>
        <div className={styles.left}>
          <Wordmark />
          <span className={styles.divider} aria-hidden />
          <ProjectSwitcher
            activeProjectId={activeProjectId}
            onSelect={(id) => navigate(`/projects/${id}`)}
            onAll={() => navigate('/')}
          />
        </div>
        <div className={styles.right}>
          {demo && (
            <span className={styles.demoTag} title="In-memory mock — no cluster">
              DEMO
            </span>
          )}
          <a
            className={styles.docsLink}
            href="https://github.com"
            target="_blank"
            rel="noreferrer"
          >
            Self-hosted
          </a>
        </div>
      </header>
      <main className={styles.content}>{children}</main>
    </div>
  );
}
