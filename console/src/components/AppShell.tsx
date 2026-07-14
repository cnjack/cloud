import {
  CaretRight,
  HardDrives,
  SquaresFour,
} from '@phosphor-icons/react';
import { useEffect, useMemo } from 'react';
import type { ReactNode } from 'react';
import { NavLink, useLocation, useMatch, useNavigate } from 'react-router-dom';
import { useDemoMode, useRole } from '../api/ApiProvider';
import { useProjects } from '../api/queries';
import type { Project } from '../api/types';
import { useOptionalAuth } from '../auth/AuthProvider';
import { IdentityChip } from './IdentityChip';
import { ThemeToggle } from './ThemeToggle';
import { Wordmark } from './Wordmark';
import styles from './AppShell.module.css';

const RECENT_PROJECTS_KEY = 'jcloud.recent-projects.v1';

function readRecentProjects(): string[] {
  try {
    const parsed = JSON.parse(window.localStorage.getItem(RECENT_PROJECTS_KEY) ?? '[]');
    return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === 'string') : [];
  } catch {
    return [];
  }
}

function rememberProject(projectId: string) {
  try {
    const next = [projectId, ...readRecentProjects().filter((id) => id !== projectId)].slice(0, 5);
    window.localStorage.setItem(RECENT_PROJECTS_KEY, JSON.stringify(next));
  } catch {
    // Storage can be unavailable in locked-down browsers; navigation still works.
  }
}

function projectInitials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return '?';
  if (words.length === 1) return words[0]!.slice(0, 2).toUpperCase();
  return `${words[0]![0]}${words[words.length - 1]![0]}`.toUpperCase();
}

function serviceLabel(project: Project): string {
  const count = project.services?.length ?? 0;
  return `${count} ${count === 1 ? 'service' : 'services'}`;
}

function Breadcrumbs() {
  const { pathname } = useLocation();
  if (pathname === '/projects/new') {
    return <><NavLink to="/projects">Projects</NavLink><span>/</span><strong>New Project</strong></>;
  }
  if (pathname.startsWith('/cluster')) {
    const leaf = pathname === '/cluster/models' ? 'Models' : pathname === '/cluster/connections' ? 'Connections' : 'Overview';
    return <><span>Cluster</span><span>/</span><strong>{leaf}</strong></>;
  }
  if (pathname === '/projects' || pathname === '/') {
    return <><span>Workspace</span><span>/</span><strong>Projects</strong></>;
  }
  return <><span>Workspace</span><span>/</span><strong>Not found</strong></>;
}

export function AppShell({ children }: { children: ReactNode }) {
  const demo = useDemoMode();
  const role = useRole();
  const auth = useOptionalAuth();
  const me = auth?.me ?? null;
  const providers = auth?.providers ?? [];
  const onSignOut = auth && !demo ? auth.logout : undefined;
  const location = useLocation();
  const projectMatch = useMatch('/projects/:projectId');
  const runMatch = useMatch('/runs/:runId');
  const activeProjectId = projectMatch?.params.projectId;
  const isProjectWorkspace = !!activeProjectId;
  const isRunWorkspace = !!runMatch;
  const isRouteWorkspace = isProjectWorkspace || isRunWorkspace;
  const projects = useProjects(!isRouteWorkspace);
  const navigate = useNavigate();

  useEffect(() => {
    if (activeProjectId) rememberProject(activeProjectId);
  }, [activeProjectId]);

  const recentProjects = useMemo(() => {
    const byId = new Map((projects.data ?? []).map((project) => [project.id, project]));
    return readRecentProjects().map((id) => byId.get(id)).filter((project): project is Project => !!project);
    // Re-evaluate after navigation as well as project data changes so returning
    // from a Project immediately exposes the honest shortcut.
  }, [projects.data, location.key]);

  if (isRouteWorkspace) {
    return (
      <div
        className={styles.workspaceShell}
        data-project-workspace={isProjectWorkspace || undefined}
        data-run-workspace={isRunWorkspace || undefined}
      >
        <main className={styles.workspaceContent}>{children}</main>
      </div>
    );
  }

  return (
    <div className={styles.shell}>
      <a className={styles.skipLink} href="#main-content">Skip to content</a>
      <aside className={styles.rail} aria-label="Global navigation">
        <div className={styles.brandRow}><Wordmark /></div>
        <div className={styles.railContext}>
          <span className={styles.eyebrow}>{location.pathname.startsWith('/cluster') ? 'Administration' : 'Workspace'}</span>
          <strong>{location.pathname.startsWith('/cluster') ? 'Cluster' : 'jcode Cloud'}</strong>
          <small>{location.pathname.startsWith('/cluster') ? 'Cluster configuration' : 'Self-hosted coding workspace'}</small>
        </div>
        <nav className={styles.railNav} aria-label="Primary">
          <NavLink to="/projects" className={({ isActive }) => `${styles.navItem} ${isActive ? styles.active : ''}`}>
            <SquaresFour size={16} aria-hidden="true" /><span>Projects</span><span className={styles.navCount}>{projects.data?.length ?? '—'}</span>
          </NavLink>
          {role === 'cluster-admin' && (
            <NavLink
              to="/cluster"
              className={({ isActive }) => `${styles.navItem} ${isActive ? styles.active : ''}`}
              data-testid="cluster-nav"
            >
              <HardDrives size={16} aria-hidden="true" /><span>Cluster</span><span className={styles.navCount}>admin</span>
            </NavLink>
          )}
        </nav>
        <section className={styles.railSection} aria-labelledby="recent-projects-label">
          <div className={styles.railSectionHead}>
            <span id="recent-projects-label">Recent projects</span><span>{recentProjects.length}</span>
          </div>
          {recentProjects.length === 0 ? (
            <p className={styles.railEmpty}>Projects you open will appear here as shortcuts.</p>
          ) : (
            <nav className={styles.recentProjects} aria-label="Recent projects">
              {recentProjects.map((project) => (
                <button key={project.id} type="button" className={styles.recentItem} onClick={() => navigate(`/projects/${project.id}`)}>
                  <span className={styles.projectMark}>{projectInitials(project.name)}</span>
                  <span className={styles.recentCopy}><strong>{project.name}</strong><small>{serviceLabel(project)}</small></span>
                  <CaretRight size={14} aria-hidden="true" />
                </button>
              ))}
            </nav>
          )}
        </section>
        <footer className={styles.railFooter}>
          <div className={styles.environment}><span>{demo ? 'demo / local' : 'orchestrator'}</span><span>v0.1.0</span></div>
          <div className={styles.identityRow}>
            <IdentityChip me={me} providers={providers} role={role} onSignOut={onSignOut} />
          </div>
        </footer>
      </aside>

      <section className={styles.surface} aria-label="jcode Cloud workspace">
        <header className={styles.utilityBar}>
          <div className={styles.breadcrumbs}><Breadcrumbs /></div>
          <div className={styles.utilityActions}>
            {demo && <span className={styles.demoTag}>DEMO</span>}
            <ThemeToggle />
          </div>
        </header>
        <main className={styles.content} id="main-content">{children}</main>
      </section>
    </div>
  );
}
