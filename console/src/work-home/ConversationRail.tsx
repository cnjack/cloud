import { ArrowRight, ChatCircle, GitBranch, MagnifyingGlass, Plus, SidebarSimple, SlidersHorizontal, TerminalWindow } from '@phosphor-icons/react';
import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useLocation } from 'react-router-dom';
import { useOptionalAuth } from '../auth/AuthProvider';
import type { Run, RunStatus, Service } from '../api/types';
import { Wordmark } from '../components/Wordmark';
import { repositoryWorkspacePath, workspaceTab } from './workspaceNavigation';
import styles from './ConversationRail.module.css';

const STATUS_KEYS: Record<RunStatus, string> = {
  queued: 'components.statusBadge.queued',
  scheduling: 'components.statusBadge.scheduling',
  running: 'components.statusBadge.running',
  awaiting_input: 'components.statusBadge.awaitingInput',
  succeeded: 'components.statusBadge.succeeded',
  failed: 'components.statusBadge.failed',
  canceled: 'components.statusBadge.canceled',
  blocked: 'components.statusBadge.blocked',
};

function runTime(value: string, locale: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat(locale, { month: 'short', day: 'numeric' }).format(date);
}

function repositoryName(repository: Service | undefined, fallback: string): string {
  return repository?.repo_owner_name || repository?.name || fallback;
}

export function ConversationRail({
  repositories,
  runs,
  isLoading,
  collapsed,
  onCollapsedChange,
  activeRepositoryId,
  error,
  onRetry,
}: {
  repositories: Service[];
  runs: Run[];
  isLoading: boolean;
  collapsed: boolean;
  onCollapsedChange: (collapsed: boolean) => void;
  activeRepositoryId?: string;
  error?: unknown;
  onRetry?: () => void;
}) {
  const { t, i18n } = useTranslation();
  const location = useLocation();
  const [query, setQuery] = useState('');
  const [mobileOpen, setMobileOpen] = useState(false);
  const [mobile, setMobile] = useState(() => window.matchMedia?.('(max-width: 832px)').matches ?? false);
  useEffect(() => {
    const query = window.matchMedia?.('(max-width: 832px)');
    if (!query) return;
    const update = () => { setMobile(query.matches); setMobileOpen(false); };
    query.addEventListener('change', update);
    return () => query.removeEventListener('change', update);
  }, []);
  const auth = useOptionalAuth();
  const normalizedQuery = query.trim().toLocaleLowerCase();
  useEffect(() => { setMobileOpen(false); }, [location.pathname, location.search]);
  useEffect(() => {
    if (!mobileOpen) return;
    const close = (event: KeyboardEvent) => { if (event.key === 'Escape') setMobileOpen(false); };
    window.addEventListener('keydown', close);
    return () => window.removeEventListener('keydown', close);
  }, [mobileOpen]);
  const latest = useMemo(() => [...runs].filter(run => run.kind !== 'review')
    .sort((a,b) => Date.parse(b.created_at) - Date.parse(a.created_at)), [runs]);
  const visibleRuns = latest.filter(run => !normalizedQuery || run.prompt.toLocaleLowerCase().includes(normalizedQuery)
    || repositoryName(repositories.find(repo => repo.id === run.service_id), '').toLocaleLowerCase().includes(normalizedQuery)).slice(0, normalizedQuery ? 30 : 6);
  const recentRepositories = useMemo(() => [...repositories].sort((a,b) => {
    const index = (id: string) => { const i = latest.findIndex(run => run.service_id === id); return i < 0 ? Number.MAX_SAFE_INTEGER : i; };
    return index(a.id)-index(b.id);
  }).slice(0,5), [repositories,latest]);
  const accountName = auth?.me?.user.display_name || t('accountHeader.account');
  const selectedTab = workspaceTab(new URLSearchParams(location.search).get('tab'));
  const toggleRail = () => {
    if (mobile) setMobileOpen(value => !value);
    else onCollapsedChange(!collapsed);
  };

  return <aside className={styles.rail} data-testid="conversation-rail" data-collapsed={collapsed || undefined} data-mobile-open={mobileOpen || undefined} aria-label={t('repositories.conversationRailAria')}>
    <header className={styles.header}>
      <span className={styles.wordmark}><Wordmark /></span>
      <button type="button" className={styles.iconButton} onClick={toggleRail} aria-expanded={mobile ? mobileOpen : !collapsed} aria-label={(mobile ? !mobileOpen : collapsed) ? t('repositories.conversationRailExpand') : t('repositories.conversationRailCollapse')}>
        <SidebarSimple size={18} />
      </button>
    </header>

    {mobileOpen && <button type="button" className={styles.drawerBackdrop} tabIndex={-1} aria-label={t('common.close')} onClick={() => setMobileOpen(false)} />}
    <div className={styles.body}>
      <Link className={styles.newConversation} to={activeRepositoryId ? repositoryWorkspacePath(activeRepositoryId) : '/repositories'} aria-label={t('repositories.conversationRailNew')}>
        <Plus size={17} /><span>{t('repositories.conversationRailNew')}</span>
      </Link>

      <label className={styles.search}>
        <MagnifyingGlass size={15} aria-hidden="true" />
        <span className={styles.srOnly}>{t('repositories.conversationRailSearchAria')}</span>
        <input type="search" value={query} onChange={(event) => setQuery(event.target.value)} aria-label={t('repositories.conversationRailSearchAria')} placeholder={t('repositories.conversationRailSearchPlaceholder')} />
      </label>

      {error != null && <div className={styles.empty} role="alert"><span>{t('cloudRefresh.navigationFailed')}</span><button type="button" onClick={onRetry}>{t('common.retry')}</button></div>}
      <section className={styles.recentRepos} aria-label={t('cloudRefresh.recentRepositories')}>
        <h2>{t('cloudRefresh.recentRepositories')}</h2>
        {recentRepositories.map(repository => <Link key={repository.id} to={repositoryWorkspacePath(repository.id, selectedTab)}
          className={styles.repositoryShortcut} aria-current={repository.id === activeRepositoryId ? 'location' : undefined}
          aria-label={t('repositories.openRepository', {name: repositoryName(repository, '')})}>
          <GitBranch size={16} /><span>{repositoryName(repository, '')}</span>
        </Link>)}
      </section>
      <Link to="/devices" className={styles.deviceLink} aria-current={location.pathname.startsWith('/devices') ? 'page' : undefined}><TerminalWindow size={17} /><span>{t('cloudRefresh.remoteDevices')}</span><ArrowRight size={13} /></Link>
      <section className={styles.conversations} aria-labelledby="conversation-rail-title">
        <h2 id="conversation-rail-title">{t('cloudRefresh.recentConversations')}</h2>
        {isLoading && !latest.length ? <ConversationRailSkeleton /> : visibleRuns.length ? <div className={styles.flatRuns}>
          {visibleRuns.map(run => <Link className={styles.run} to={`/runs/${run.id}`} key={run.id} aria-label={run.prompt} aria-current={location.pathname === `/runs/${run.id}` ? 'page' : undefined}>
            <span className={styles.runCopy}><strong>{run.prompt || t('repositories.conversationSection')}</strong><small><i data-status={run.status} /><span>{t(STATUS_KEYS[run.status])}</span><time dateTime={run.created_at}>{runTime(run.created_at, i18n.resolvedLanguage || i18n.language)}</time></small></span>
          </Link>)}
        </div> : <div className={styles.empty}><ChatCircle size={18} /><strong>{normalizedQuery ? t('repositories.conversationRailNoMatch') : t('repositories.conversationRailEmptyTitle')}</strong></div>}
      </section>
      <footer className={styles.footer}>
        <Link to={`/repositories?picker=1${activeRepositoryId ? `&repository=${encodeURIComponent(activeRepositoryId)}` : ''}`}><GitBranch size={16}/><span>{t('cloudRefresh.allRepositories')}</span><ArrowRight size={13}/></Link>
        <Link to="/account/settings" aria-current={location.pathname === '/account/settings' ? 'page' : undefined}><SlidersHorizontal size={16}/><span>{t('accountHeader.settings')}</span></Link>
        <Link className={styles.identity} to="/account/settings?section=profile"><span className={styles.avatar}>{accountName.slice(0,2).toUpperCase()}</span><span><strong>{accountName}</strong><small>{t('cloudRefresh.personalWorkspace')}</small></span></Link>
      </footer>
    </div>
  </aside>;
}

function ConversationRailSkeleton() {
  return <div className={styles.skeleton} aria-hidden="true">
    {[0, 1, 2].map((item) => <span key={item}><i /><i /></span>)}
  </div>;
}
