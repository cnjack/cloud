import { ArrowRight, MagnifyingGlass, Plus } from '@phosphor-icons/react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Link } from 'react-router-dom';
import { useProjects } from '../api/queries';
import type { Project } from '../api/types';
import { ActionLink, PageHeader, SurfaceInner } from '../components/PageLayout';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { timeAgo } from '../lib/format';
import styles from './ProjectsPage.module.css';

function initials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return '?';
  if (words.length === 1) return words[0]!.slice(0, 2).toUpperCase();
  return `${words[0]![0]}${words[words.length - 1]![0]}`.toUpperCase();
}

function serviceNames(project: Project, t: TFunction): string {
  const names = (project.services ?? []).map((service) => service.name);
  return names.length > 0 ? names.join(' · ') : t('projects.noServicesConnected');
}

function serviceCount(project: Project, t: TFunction): string {
  const count = project.services?.length ?? 0;
  return t('projects.serviceCount', { count });
}

export function ProjectsPage() {
  const { t } = useTranslation();
  const projects = useProjects();
  const [search, setSearch] = useState('');
  const visible = useMemo(() => {
    const query = search.trim().toLowerCase();
    if (!query) return projects.data ?? [];
    return (projects.data ?? []).filter((project) => {
      const haystack = [project.name, ...(project.services ?? []).flatMap((service) => [service.name, service.repo_owner_name ?? '', service.raw_repo_url ?? ''])].join(' ').toLowerCase();
      return haystack.includes(query);
    });
  }, [projects.data, search]);

  return (
    <SurfaceInner>
      <PageHeader
        eyebrow={t('projects.eyebrow')}
        title={t('projects.title')}
        description={t('projects.description')}
        actions={(projects.data?.length ?? 0) > 0 ? (
          <ActionLink to="/projects/new" variant="primary"><Plus size={14} aria-hidden="true" />{t('projects.newProject')}</ActionLink>
        ) : undefined}
      />

      {projects.isLoading ? (
        <div className={styles.state}><LoadingBlock label={t('projects.loadingProjects')} /></div>
      ) : projects.isError ? (
        <div className={styles.state}><ErrorBlock error={projects.error} onRetry={() => projects.refetch()} title={t('projects.loadError')} /></div>
      ) : (projects.data?.length ?? 0) === 0 ? (
        <section className={styles.emptyStage} data-testid="projects-empty" aria-labelledby="empty-projects-title">
          <div className={styles.emptyCopy}>
            <span className={styles.eyebrow}>{t('projects.empty.eyebrow')}</span>
            <h2 id="empty-projects-title">{t('projects.empty.title')}</h2>
            <p>{t('projects.empty.body')}</p>
            <div><ActionLink to="/projects/new" variant="primary"><Plus size={14} aria-hidden="true" />{t('projects.empty.cta')}</ActionLink></div>
          </div>
          <div className={styles.blueprint} aria-hidden="true" />
        </section>
      ) : (
        <div className={styles.layout}>
          <section aria-labelledby="project-list-title">
            <div className={styles.sectionHeading}>
              <div><h2 id="project-list-title">{t('projects.listTitle')}</h2><p><span data-testid="project-visible-count">{visible.length}</span> {t('projects.visible')}</p></div>
            </div>
            <div className={styles.toolbar}>
              <label className={styles.search}>
                <MagnifyingGlass size={14} aria-hidden="true" />
                <span className={styles.srOnly}>{t('projects.searchProjects')}</span>
                <input type="search" aria-label={t('projects.searchProjects')} placeholder={t('projects.searchPlaceholder')} value={search} onChange={(event) => setSearch(event.target.value)} />
              </label>
              <span className={styles.updated}>{t('projects.updatedJustNow')}</span>
            </div>
            {visible.length === 0 ? (
              <div className={styles.searchEmpty} role="status"><strong>{t('projects.searchEmptyTitle')}</strong><p>{t('projects.searchEmptyBody')}</p></div>
            ) : (
              <ul className={styles.list}>
                {visible.map((project) => (
                  <li key={project.id}>
                    <Link className={styles.row} to={`/projects/${project.id}`} data-testid="project-row">
                      <span className={styles.mark}>{initials(project.name)}</span>
                      <span className={styles.rowMain}>
                        <span className={styles.rowTitle}><strong>{project.name}</strong><span className={styles.tag}>{serviceCount(project, t)}</span></span>
                        <span className={styles.rowMeta}><span className={styles.mono}>{serviceNames(project, t)}</span><span className={styles.created}>{t('projects.createdAt', { time: timeAgo(project.created_at) })}</span></span>
                      </span>
                      <span className={styles.rowSide}><span>{t('projects.openWorkspace')}</span><ArrowRight size={16} aria-hidden="true" /></span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <aside className={styles.guide}>
            <span className={styles.eyebrow}>{t('projects.guide.eyebrow')}</span>
            <h2>{t('projects.guide.title')}</h2>
            <p>{t('projects.guide.body')}</p>
            <ol>
              <li><span><strong>{t('projects.guide.step1Title')}</strong><small>{t('projects.guide.step1Desc')}</small></span></li>
              <li><span><strong>{t('projects.guide.step2Title')}</strong><small>{t('projects.guide.step2Desc')}</small></span></li>
              <li><span><strong>{t('projects.guide.step3Title')}</strong><small>{t('projects.guide.step3Desc')}</small></span></li>
            </ol>
          </aside>
        </div>
      )}
    </SurfaceInner>
  );
}
