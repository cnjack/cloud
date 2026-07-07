/*
 * AppShell — top nav (wordmark + project switcher + demo tag + identity chip)
 * and the routed content region. Responsive: primary ≥1024px, usable at 768px.
 */
import { NavLink, useNavigate, useParams } from 'react-router-dom';
import type { ReactNode } from 'react';
import { Wordmark } from './Wordmark';
import { ProjectSwitcher } from './ProjectSwitcher';
import { IdentityChip } from './IdentityChip';
import { ThemeToggle } from './ThemeToggle';
import { useDemoMode, useRole } from '../api/ApiProvider';
import { useOptionalAuth } from '../auth/AuthProvider';
import styles from './AppShell.module.css';

export function AppShell({ children }: { children: ReactNode }) {
  const demo = useDemoMode();
  const role = useRole();
  // Sign-out only makes sense for a real verified session (gate mounted, not demo).
  const auth = useOptionalAuth();
  const me = auth?.me ?? null;
  const providers = auth?.providers ?? [];
  const onSignOut = auth && !demo ? auth.logout : undefined;
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
          {/* Cluster view is the cluster-admin home; hidden for project-admin
              (presentation-only gating until real authz exists). */}
          {role === 'cluster-admin' && (
            <NavLink
              to="/system"
              className={({ isActive }) =>
                [styles.navLink, isActive && styles.navLinkActive]
                  .filter(Boolean)
                  .join(' ')
              }
              data-testid="cluster-nav"
            >
              Cluster
            </NavLink>
          )}
          {demo && (
            <span className={styles.demoTag} title="In-memory mock — no cluster">
              DEMO
            </span>
          )}
          <ThemeToggle />
          <IdentityChip me={me} providers={providers} role={role} onSignOut={onSignOut} />
        </div>
      </header>
      <main className={styles.content}>{children}</main>
    </div>
  );
}
