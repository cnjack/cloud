import { ArrowRight, MagnifyingGlass, Plus } from '@phosphor-icons/react';
import { useMemo, useState } from 'react';
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

function serviceNames(project: Project): string {
  const names = (project.services ?? []).map((service) => service.name);
  return names.length > 0 ? names.join(' · ') : 'No Services connected';
}

function serviceCount(project: Project): string {
  const count = project.services?.length ?? 0;
  return `${count} ${count === 1 ? 'service' : 'services'}`;
}

export function ProjectsPage() {
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
        eyebrow="Your workspace"
        title="Projects"
        description="A Project groups the people, Services, model access, and automation policy that ship one product."
        actions={(projects.data?.length ?? 0) > 0 ? (
          <ActionLink to="/projects/new" variant="primary"><Plus size={14} aria-hidden="true" />New Project</ActionLink>
        ) : undefined}
      />

      {projects.isLoading ? (
        <div className={styles.state}><LoadingBlock label="Loading projects…" /></div>
      ) : projects.isError ? (
        <div className={styles.state}><ErrorBlock error={projects.error} onRetry={() => projects.refetch()} title="Couldn't load projects" /></div>
      ) : (projects.data?.length ?? 0) === 0 ? (
        <section className={styles.emptyStage} data-testid="projects-empty" aria-labelledby="empty-projects-title">
          <div className={styles.emptyCopy}>
            <span className={styles.eyebrow}>Start with the boundary</span>
            <h2 id="empty-projects-title">No Projects yet.</h2>
            <p>Create the first Project as an empty container. The next screen lets you authorize repositories as Services without pretending unavailable providers are connected.</p>
            <div><ActionLink to="/projects/new" variant="primary"><Plus size={14} aria-hidden="true" />Create first Project</ActionLink></div>
          </div>
          <div className={styles.blueprint} aria-hidden="true" />
        </section>
      ) : (
        <div className={styles.layout}>
          <section aria-labelledby="project-list-title">
            <div className={styles.sectionHeading}>
              <div><h2 id="project-list-title">All Projects</h2><p><span data-testid="project-visible-count">{visible.length}</span> visible</p></div>
            </div>
            <div className={styles.toolbar}>
              <label className={styles.search}>
                <MagnifyingGlass size={14} aria-hidden="true" />
                <span className={styles.srOnly}>Search projects</span>
                <input type="search" aria-label="Search projects" placeholder="Search name or Service…" value={search} onChange={(event) => setSearch(event.target.value)} />
              </label>
              <span className={styles.updated}>updated just now</span>
            </div>
            {visible.length === 0 ? (
              <div className={styles.searchEmpty} role="status"><strong>No matching Projects.</strong><p>Try a Project, Service, or repository name.</p></div>
            ) : (
              <ul className={styles.list}>
                {visible.map((project) => (
                  <li key={project.id}>
                    <Link className={styles.row} to={`/projects/${project.id}`} data-testid="project-row">
                      <span className={styles.mark}>{initials(project.name)}</span>
                      <span className={styles.rowMain}>
                        <span className={styles.rowTitle}><strong>{project.name}</strong><span className={styles.tag}>{serviceCount(project)}</span></span>
                        <span className={styles.rowMeta}><span className={styles.mono}>{serviceNames(project)}</span><span className={styles.created}>Created {timeAgo(project.created_at)}</span></span>
                      </span>
                      <span className={styles.rowSide}><span>Open workspace</span><ArrowRight size={16} aria-hidden="true" /></span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <aside className={styles.guide}>
            <span className={styles.eyebrow}>How Projects work</span>
            <h2>Keep ownership broad. Keep execution explicit.</h2>
            <p>A Project is the long-lived boundary; each repository becomes a Service with its own model and automation wiring.</p>
            <ol>
              <li><span><strong>Create the boundary</strong><small>Name the product or team.</small></span></li>
              <li><span><strong>Add Services</strong><small>Authorize repositories explicitly.</small></span></li>
              <li><span><strong>Start Tasks</strong><small>Only after dependencies are ready.</small></span></li>
            </ol>
          </aside>
        </div>
      )}
    </SurfaceInner>
  );
}
