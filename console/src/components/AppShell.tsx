import {
  CaretRight,
  Devices,
  HardDrives,
  SquaresFour,
} from '@phosphor-icons/react';
import { useEffect, useMemo } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { NavLink, useLocation, useMatch, useNavigate } from 'react-router-dom';
import { useDemoMode, useRole } from '../api/ApiProvider';
import { useProjects } from '../api/queries';
import type { Project } from '../api/types';
import { useOptionalAuth } from '../auth/AuthProvider';
import { LanguageToggle } from './LanguageToggle';
import { RailAccountFooter } from './RailAccountFooter';
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

function Breadcrumbs() {
  const { pathname } = useLocation();
  const { t } = useTranslation();
  if (pathname === '/projects/new') {
    return <><NavLink to="/projects">{t('shell.crumbProjects')}</NavLink><span>/</span><strong>{t('shell.crumbNewProject')}</strong></>;
  }
  if (pathname.startsWith('/cluster')) {
    const leaf = pathname === '/cluster/models' ? t('shell.crumbModels') : pathname === '/cluster/connections' ? t('shell.crumbConnections') : t('shell.crumbOverview');
    return <><span>{t('shell.crumbCluster')}</span><span>/</span><strong>{leaf}</strong></>;
  }
  if (pathname.startsWith('/devices')) {
    return <><span>{t('shell.crumbWorkspace')}</span><span>/</span><strong>{t('shell.devices')}</strong></>;
  }
  if (pathname === '/projects' || pathname === '/') {
    return <><span>{t('shell.crumbWorkspace')}</span><span>/</span><strong>{t('shell.crumbProjects')}</strong></>;
  }
  return <><span>{t('shell.crumbWorkspace')}</span><span>/</span><strong>{t('shell.crumbNotFound')}</strong></>;
}

export function AppShell({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
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

  const isCluster = location.pathname.startsWith('/cluster');
  return (
    <div className={styles.shell}>
      <a className={styles.skipLink} href="#main-content">{t('shell.skipToContent')}</a>
      <aside className={styles.rail} aria-label={t('shell.globalNav')}>
        <div className={styles.brandRow}><Wordmark /></div>
        <div className={styles.railContext}>
          <span className={styles.eyebrow}>{isCluster ? t('shell.adminEyebrow') : t('shell.workspaceEyebrow')}</span>
          <strong>{isCluster ? t('shell.clusterTitle') : t('shell.cloudTitle')}</strong>
          <small>{isCluster ? t('shell.clusterSubtitle') : t('shell.cloudSubtitle')}</small>
        </div>
        <nav className={styles.railNav} aria-label={t('shell.primaryNav')}>
          <NavLink to="/projects" className={({ isActive }) => `${styles.navItem} ${isActive ? styles.active : ''}`}>
            <SquaresFour size={16} aria-hidden="true" /><span>{t('shell.projects')}</span><span className={styles.navCount}>{projects.data?.length ?? '—'}</span>
          </NavLink>
          <NavLink to="/devices" className={({ isActive }) => `${styles.navItem} ${isActive ? styles.active : ''}`}>
            <Devices size={16} aria-hidden="true" /><span>{t('shell.devices')}</span>
          </NavLink>
          {role === 'cluster-admin' && (
            <NavLink
              to="/cluster"
              className={({ isActive }) => `${styles.navItem} ${isActive ? styles.active : ''}`}
              data-testid="cluster-nav"
            >
              <HardDrives size={16} aria-hidden="true" /><span>{t('shell.cluster')}</span><span className={styles.navCount}>{t('shell.adminBadge')}</span>
            </NavLink>
          )}
        </nav>
        <section className={styles.railSection} aria-labelledby="recent-projects-label">
          <div className={styles.railSectionHead}>
            <span id="recent-projects-label">{t('shell.recentProjects')}</span><span>{recentProjects.length}</span>
          </div>
          {recentProjects.length === 0 ? (
            <p className={styles.railEmpty}>{t('shell.recentEmpty')}</p>
          ) : (
            <nav className={styles.recentProjects} aria-label={t('shell.recentAria')}>
              {recentProjects.map((project) => (
                <button key={project.id} type="button" className={styles.recentItem} onClick={() => navigate(`/projects/${project.id}`)}>
                  <span className={styles.projectMark}>{projectInitials(project.name)}</span>
                  <span className={styles.recentCopy}><strong>{project.name}</strong><small>{t('shell.services', { count: project.services?.length ?? 0 })}</small></span>
                  <CaretRight size={14} aria-hidden="true" />
                </button>
              ))}
            </nav>
          )}
        </section>
        <footer className={styles.railFooter}>
          <RailAccountFooter
            demo={demo}
            me={me}
            providers={providers}
            role={role}
            onSignOut={onSignOut}
            testId="app-rail-footer"
          />
        </footer>
      </aside>

      <section className={styles.surface} aria-label={t('shell.workspaceAria')}>
        <header className={styles.utilityBar}>
          <div className={styles.breadcrumbs}><Breadcrumbs /></div>
          <div className={styles.utilityActions}>
            {demo && <span className={styles.demoTag}>{t('shell.demoTag')}</span>}
            <LanguageToggle />
            <ThemeToggle />
          </div>
        </header>
        <main className={styles.content} id="main-content">{children}</main>
      </section>
    </div>
  );
}
