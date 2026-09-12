import {
  CaretDown,
  CaretRight,
  ChatCircle,
  MagnifyingGlass,
  Plus,
  SidebarSimple,
} from '@phosphor-icons/react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useLocation } from 'react-router-dom';
import type { Run, RunStatus, Service } from '../api/types';
import { Wordmark } from '../components/Wordmark';
import { WorkspaceIdentity } from '../components/WorkspaceIdentity';
import { repositoryWorkspacePath, workspaceTab } from './workspaceNavigation';
import styles from './ConversationRail.module.css';

interface ConversationGroup {
  id: string;
  name: string;
  runs: Run[];
  newestAt: number;
  available: boolean;
}

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
}: {
  repositories: Service[];
  runs: Run[];
  isLoading: boolean;
  collapsed: boolean;
  onCollapsedChange: (collapsed: boolean) => void;
  activeRepositoryId?: string;
}) {
  const { t, i18n } = useTranslation();
  const location = useLocation();
  const [query, setQuery] = useState('');
  const [closedGroups, setClosedGroups] = useState<Set<string>>(() => new Set());
  const normalizedQuery = query.trim().toLocaleLowerCase();

  const groups = useMemo(() => {
    const services = new Map(repositories.map((repository) => [repository.id, repository]));
    const grouped = new Map<string, Run[]>();
    for (const run of runs) {
      if (run.kind === 'review') continue;
      const id = run.service_id || 'unavailable';
      const existing = grouped.get(id) ?? [];
      existing.push(run);
      grouped.set(id, existing);
    }
    return [...grouped.entries()].map(([id, repositoryRuns]): ConversationGroup => {
      const sortedRuns = [...repositoryRuns].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
      return {
        id,
        name: repositoryName(services.get(id), t('repositories.conversationRailRepositoryUnavailable')),
        runs: sortedRuns,
        newestAt: Date.parse(sortedRuns[0]?.created_at ?? '') || 0,
        available: services.has(id),
      };
    }).sort((a, b) => b.newestAt - a.newestAt);
  }, [repositories, runs, t]);

  const visibleGroups = useMemo(() => {
    if (!normalizedQuery) return groups;
    return groups.flatMap((group) => {
      const repositoryMatches = group.name.toLocaleLowerCase().includes(normalizedQuery);
      const matchingRuns = repositoryMatches
        ? group.runs
        : group.runs.filter((run) => run.prompt.toLocaleLowerCase().includes(normalizedQuery));
      return matchingRuns.length > 0 ? [{ ...group, runs: matchingRuns }] : [];
    });
  }, [groups, normalizedQuery]);

  const toggleGroup = (id: string) => {
    setClosedGroups((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  return <aside className={styles.rail} data-testid="conversation-rail" data-collapsed={collapsed || undefined} aria-label={t('repositories.conversationRailAria')}>
    <header className={styles.header}>
      <span className={styles.wordmark}><Wordmark /></span>
      <button type="button" className={styles.iconButton} onClick={() => onCollapsedChange(!collapsed)} aria-label={collapsed ? t('repositories.conversationRailExpand') : t('repositories.conversationRailCollapse')}>
        <SidebarSimple size={18} />
      </button>
    </header>

    <div className={styles.body}>
      <Link className={styles.newConversation} to="/repositories" aria-label={t('repositories.conversationRailNew')}>
        <Plus size={17} /><span>{t('repositories.conversationRailNew')}</span>
      </Link>

      <label className={styles.search}>
        <MagnifyingGlass size={15} aria-hidden="true" />
        <span className={styles.srOnly}>{t('repositories.conversationRailSearchAria')}</span>
        <input type="search" value={query} onChange={(event) => setQuery(event.target.value)} aria-label={t('repositories.conversationRailSearchAria')} placeholder={t('repositories.conversationRailSearchPlaceholder')} />
      </label>

      <section className={styles.conversations} aria-labelledby="conversation-rail-title">
        <h2 id="conversation-rail-title">{t('repositories.conversationRailByRepository')}</h2>
        {isLoading && groups.length === 0 ? <ConversationRailSkeleton /> : visibleGroups.length > 0 ? (
          <div className={styles.groups}>
            {visibleGroups.map((group) => {
              const open = normalizedQuery.length > 0 || !closedGroups.has(group.id);
              return <section className={styles.group} key={group.id}>
                <div className={styles.groupHeading} data-active={group.id === activeRepositoryId || undefined}>
                <button type="button" className={styles.groupToggle} aria-expanded={open} aria-label={t('repositories.toggleConversations', { name: group.name })} aria-controls={`rail-conversations-${group.id}`} onClick={() => toggleGroup(group.id)}>
                  {open ? <CaretDown size={13} /> : <CaretRight size={13} />}
                </button>
                {group.available ? <Link className={styles.repositoryLink}
                  to={repositoryWorkspacePath(group.id, workspaceTab(new URLSearchParams(location.search).get('tab')))}
                  aria-label={t('repositories.openRepository', { name: group.name })}
                  aria-current={group.id === activeRepositoryId ? 'location' : undefined}>
                  <WorkspaceIdentity name={group.name} />
                  <small>{group.runs.length}</small>
                </Link> : <span className={styles.repositoryLink}><WorkspaceIdentity name={group.name} /><small>{group.runs.length}</small></span>}
                </div>
                {open && <div className={styles.runList} id={`rail-conversations-${group.id}`}>
                  {group.runs.map((run) => {
                    const path = `/runs/${run.id}`;
                    const active = location.pathname === path;
                    return <Link className={styles.run} to={path} key={run.id} aria-label={run.prompt} aria-current={active ? 'page' : undefined}>
                    <span className={styles.runIcon}><ChatCircle size={13} /></span>
                    <span className={styles.runCopy}><strong>{run.prompt}</strong><small><i data-status={run.status} /><span>{t(STATUS_KEYS[run.status])}</span><time dateTime={run.created_at}>{runTime(run.created_at, i18n.resolvedLanguage || i18n.language)}</time></small></span>
                    </Link>;
                  })}
                </div>}
              </section>;
            })}
          </div>
        ) : <div className={styles.empty}>
          <ChatCircle size={18} />
          <strong>{normalizedQuery ? t('repositories.conversationRailNoMatch') : t('repositories.conversationRailEmptyTitle')}</strong>
          {!normalizedQuery && <span>{t('repositories.conversationRailEmptyDescription')}</span>}
        </div>}
      </section>
    </div>
  </aside>;
}

function ConversationRailSkeleton() {
  return <div className={styles.skeleton} aria-hidden="true">
    {[0, 1, 2].map((item) => <span key={item}><i /><i /></span>)}
  </div>;
}
