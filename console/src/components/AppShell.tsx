import {
  CaretRight,
  Devices,
  GitBranch,
  GitPullRequest,
  HardDrives,
} from '@phosphor-icons/react';
import { useEffect, useMemo } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, matchPath, NavLink, useLocation, useMatch, useNavigate } from 'react-router-dom';
import { useDemoMode, useRole } from '../api/ApiProvider';
import { useProjects, useRepositories } from '../api/queries';
import type { Project, Service } from '../api/types';
import { useOptionalAuth } from '../auth/AuthProvider';
import { LanguageToggle } from './LanguageToggle';
import { RailAccountFooter } from './RailAccountFooter';
import { ThemeToggle } from './ThemeToggle';
import { Wordmark } from './Wordmark';
import styles from './AppShell.module.css';

const RECENT_PROJECTS_KEY = 'jcloud.recent-projects.v1';
const RECENT_REPOSITORIES_KEY = 'jcloud.recent-repositories.v1';
const PROJECT_CHILD_PATTERNS = [
  '/projects/:projectId/plugins/:provider',
  '/projects/:projectId/automations/new',
  '/projects/:projectId/automations/:automationId/edit',
  '/projects/:projectId/automations/:automationId',
] as const;
const CLUSTER_PATHS = ['/cluster', '/cluster/models', '/cluster/connections'] as const;

function exactMatch(pattern: string, pathname: string) {
  return matchPath({ path: pattern, end: true }, pathname);
}

function knownProjectChildId(pathname: string): string | undefined {
  for (const pattern of PROJECT_CHILD_PATTERNS) {
    const match = exactMatch(pattern, pathname);
    if (match?.params.projectId) return match.params.projectId;
  }
  return undefined;
}

function isKnownClusterPath(pathname: string): boolean {
  return CLUSTER_PATHS.some((path) => !!exactMatch(path, pathname));
}

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

function readRecentRepositories(): string[] {
  try {
    const parsed = JSON.parse(window.localStorage.getItem(RECENT_REPOSITORIES_KEY) ?? '[]');
    return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === 'string') : [];
  } catch {
    return [];
  }
}

function rememberRepository(repositoryId: string) {
  try {
    const next = [repositoryId, ...readRecentRepositories().filter((id) => id !== repositoryId)].slice(0, 5);
    window.localStorage.setItem(RECENT_REPOSITORIES_KEY, JSON.stringify(next));
  } catch {
    // Navigation remains available when storage is blocked.
  }
}

function projectInitials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return '?';
  if (words.length === 1) return words[0]!.slice(0, 2).toUpperCase();
  return `${words[0]![0]}${words[words.length - 1]![0]}`.toUpperCase();
}

function Breadcrumbs({ projects }: { projects: Project[] }) {
  const { pathname, search } = useLocation();
  const { t } = useTranslation();

  const projectTrail = (projectId: string, leaf: string) => {
    const projectName = projects.find((project) => project.id === projectId)?.name
      ?? t('shell.crumbProject');
    return <>
      <Link to="/projects">{t('shell.crumbProjects')}</Link><span>/</span>
      <Link to={`/projects/${encodeURIComponent(projectId)}`}>{projectName}</Link><span>/</span>
      <strong>{leaf}</strong>
    </>;
  };

  if (exactMatch('/projects/new', pathname)) {
    return <><Link to="/projects">{t('shell.crumbProjects')}</Link><span>/</span><strong>{t('shell.crumbNewProject')}</strong></>;
  }

  const pluginMatch = exactMatch('/projects/:projectId/plugins/:provider', pathname);
  if (pluginMatch?.params.projectId) {
    const provider = pluginMatch.params.provider ?? '';
    const providerLabel = ({ github: 'GitHub', gitlab: 'GitLab', gitea: 'Gitea', jtype: 'JType Kanban' } as Record<string, string>)[provider]
      ?? t('plugins.projectPlugin');
    return projectTrail(pluginMatch.params.projectId, providerLabel);
  }

  const automationNewMatch = exactMatch('/projects/:projectId/automations/new', pathname);
  if (automationNewMatch?.params.projectId) {
    const reviewPreset = new URLSearchParams(search).get('preset') === 'review';
    return projectTrail(
      automationNewMatch.params.projectId,
      reviewPreset ? t('automationEditor.review.title') : t('automationEditor.createTitle'),
    );
  }

  const automationEditMatch = exactMatch('/projects/:projectId/automations/:automationId/edit', pathname);
  if (automationEditMatch?.params.projectId) {
    return projectTrail(automationEditMatch.params.projectId, t('automationEditor.editTitle'));
  }

  const automationMatch = exactMatch('/projects/:projectId/automations/:automationId', pathname);
  if (automationMatch?.params.projectId) {
    return projectTrail(automationMatch.params.projectId, t('projectAutomations.title'));
  }

  if (isKnownClusterPath(pathname)) {
    const leaf = exactMatch('/cluster/models', pathname)
      ? t('shell.crumbModels')
      : exactMatch('/cluster/connections', pathname)
        ? t('shell.crumbConnections')
        : t('shell.crumbOverview');
    return <><Link to="/cluster">{t('shell.crumbCluster')}</Link><span>/</span><strong>{leaf}</strong></>;
  }

  if (exactMatch('/devices', pathname)) {
    return <><span>{t('shell.crumbWorkspace')}</span><span>/</span><strong>{t('shell.devices')}</strong></>;
  }

  if (exactMatch('/devices/guide', pathname)) {
    return <><Link to="/devices">{t('shell.devices')}</Link><span>/</span><strong>{t('device.guide.entry')}</strong></>;
  }

  const sessionMatch = exactMatch('/devices/:deviceId/sessions/:sessionId', pathname);
  if (sessionMatch?.params.deviceId) {
    return <>
      <Link to="/devices">{t('shell.devices')}</Link><span>/</span>
      <Link to={`/devices/${encodeURIComponent(sessionMatch.params.deviceId)}`}>{t('shell.crumbDevice')}</Link><span>/</span>
      <strong>{t('shell.crumbSession')}</strong>
    </>;
  }

  if (exactMatch('/devices/:deviceId', pathname)) {
    return <><Link to="/devices">{t('shell.devices')}</Link><span>/</span><strong>{t('shell.crumbDevice')}</strong></>;
  }

  if (exactMatch('/projects', pathname) || exactMatch('/', pathname)) {
    return <><span>{t('shell.crumbWorkspace')}</span><span>/</span><strong>{t('shell.crumbProjects')}</strong></>;
  }

  if (exactMatch('/repositories', pathname)) {
    return <><span>{t('shell.crumbWorkspace')}</span><span>/</span><strong>{t('repositories.title')}</strong></>;
  }

  if (exactMatch('/connections/repositories', pathname)) {
    return <><Link to="/repositories">{t('repositories.title')}</Link><span>/</span><strong>{t('repositories.connectionsTitle')}</strong></>;
  }

  if (exactMatch('/code-reviews', pathname)) {
    return <><span>{t('shell.crumbWorkspace')}</span><span>/</span><strong>{t('repositories.codeReviews')}</strong></>;
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
  const repositoryMatch = useMatch('/repositories/:repositoryId');
  const runMatch = useMatch('/runs/:runId');
  const projectWorkspaceId = projectMatch?.params.projectId === 'new'
    ? undefined
    : projectMatch?.params.projectId;
  const activeProjectId = projectWorkspaceId ?? knownProjectChildId(location.pathname);
  const repositoryRouteId = repositoryMatch?.params.repositoryId;
  const repositoryConnect = repositoryRouteId === 'connect';
  const activeRepositoryId = repositoryConnect ? undefined : repositoryRouteId;
  const isProjectWorkspace = !!projectWorkspaceId;
  const isRepositoryWorkspace = !!activeRepositoryId || repositoryConnect;
  const isRunWorkspace = !!runMatch;
  const isRouteWorkspace = isProjectWorkspace || isRepositoryWorkspace || isRunWorkspace;
  const isDeviceAuthorization = !!exactMatch('/device', location.pathname);
  const projects = useProjects(!isRouteWorkspace && !isDeviceAuthorization);
  const repositories = useRepositories(!isRouteWorkspace && !isDeviceAuthorization);
  const navigate = useNavigate();

  useEffect(() => {
    if (activeProjectId) rememberProject(activeProjectId);
  }, [activeProjectId]);

  useEffect(() => {
    if (activeRepositoryId) rememberRepository(activeRepositoryId);
  }, [activeRepositoryId]);

  const recentRepositories = useMemo(() => {
    const byId = new Map((repositories.data ?? []).map((repository) => [repository.id, repository]));
    return readRecentRepositories().map((id) => byId.get(id)).filter((repository): repository is Service => !!repository);
  }, [repositories.data, location.key]);

  if (isRouteWorkspace) {
    return (
      <div
        className={styles.workspaceShell}
        data-project-workspace={isProjectWorkspace || undefined}
        data-repository-workspace={isRepositoryWorkspace || undefined}
        data-run-workspace={isRunWorkspace || undefined}
      >
        <main className={styles.workspaceContent}>{children}</main>
      </div>
    );
  }

  // Device-code authorization is a security boundary, not a workspace page.
  // Keep it inside OnboardingGate (so an unauthenticated visitor still signs
  // in and returns here), but never surround it with project navigation.
  if (isDeviceAuthorization) {
    return <div className={styles.standaloneAuth} data-device-authorization="true">{children}</div>;
  }

  const isCluster = isKnownClusterPath(location.pathname);
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
          <NavLink to="/repositories" className={({ isActive }) => `${styles.navItem} ${isActive ? styles.active : ''}`}>
            <GitBranch size={16} aria-hidden="true" /><span>{t('repositories.title')}</span><span className={styles.navCount}>{repositories.data?.length ?? '—'}</span>
          </NavLink>
          <NavLink to="/devices" className={({ isActive }) => `${styles.navItem} ${isActive ? styles.active : ''}`}>
            <Devices size={16} aria-hidden="true" /><span>{t('shell.devices')}</span>
          </NavLink>
          <NavLink to="/code-reviews" className={({ isActive }) => `${styles.navItem} ${isActive ? styles.active : ''}`}>
            <GitPullRequest size={16} aria-hidden="true" /><span>{t('repositories.codeReviews')}</span>
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
        <section className={styles.railSection} aria-labelledby="recent-repositories-label">
          <div className={styles.railSectionHead}>
            <span id="recent-repositories-label">{t('repositories.title')}</span><span>{recentRepositories.length}</span>
          </div>
          {recentRepositories.length === 0 ? (
            <p className={styles.railEmpty}>{t('shell.recentEmpty')}</p>
          ) : (
            <nav className={styles.recentProjects} aria-label={t('repositories.title')}>
              {recentRepositories.map((repository) => (
                <button key={repository.id} type="button" className={styles.recentItem} onClick={() => navigate(`/repositories/${repository.id}`)}>
                  <span className={styles.projectMark}>{projectInitials(repository.name)}</span>
                  <span className={styles.recentCopy}><strong>{repository.repo_owner_name || repository.name}</strong><small>{repository.default_branch}</small></span>
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
          <div className={styles.breadcrumbs}><Breadcrumbs projects={projects.data ?? []} /></div>
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
