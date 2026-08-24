import { ArrowRight, Gear, GitBranch, MagnifyingGlass, Plus } from '@phosphor-icons/react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { useRepositories } from '../api/queries';
import { ActionLink, PageHeader, SurfaceInner } from '../components/PageLayout';
import { ErrorBlock, LoadingBlock } from '../components/States';
import { serviceProviderLabel, serviceSource } from '../project-workspace/presentation';
import styles from './ProjectsPage.module.css';

export function RepositoriesPage() {
  const { t } = useTranslation();
  const repositories = useRepositories();
  const [search, setSearch] = useState('');
  const visible = useMemo(() => {
    const query = search.trim().toLowerCase();
    if (!query) return repositories.data ?? [];
    return (repositories.data ?? []).filter((repository) =>
      [repository.name, repository.repo_owner_name ?? '', repository.raw_repo_url ?? '']
        .join(' ')
        .toLowerCase()
        .includes(query));
  }, [repositories.data, search]);

  return (
    <SurfaceInner>
      <PageHeader
        eyebrow={t('repositories.eyebrow')}
        title={t('repositories.title')}
        description={t('repositories.description')}
        actions={(
          <ActionLink to="/repositories/connect" variant="primary">
            <Plus size={14} aria-hidden="true" />{t('repositories.connect')}
          </ActionLink>
        )}
      />

      {repositories.isLoading ? (
        <div className={styles.state}><LoadingBlock label={t('repositories.loading')} /></div>
      ) : repositories.isError ? (
        <div className={styles.state}>
          <ErrorBlock
            error={repositories.error}
            onRetry={() => void repositories.refetch()}
            title={t('repositories.loadError')}
          />
        </div>
      ) : visible.length === 0 && !search ? (
        <section className={styles.emptyStage} data-testid="repositories-empty">
          <div className={styles.emptyCopy}>
            <span className={styles.eyebrow}>{t('repositories.emptyEyebrow')}</span>
            <h2>{t('repositories.emptyTitle')}</h2>
            <p>{t('repositories.emptyBody')}</p>
            <div><ActionLink to="/repositories/connect" variant="primary">{t('repositories.connect')}</ActionLink></div>
          </div>
          <div className={styles.blueprint} aria-hidden="true" />
        </section>
      ) : (
        <section aria-labelledby="repository-list-title">
          <div className={styles.sectionHeading}>
            <div>
              <h2 id="repository-list-title">{t('repositories.listTitle')}</h2>
              <p><span data-testid="repository-visible-count">{visible.length}</span> {t('repositories.visible')}</p>
            </div>
          </div>
          <div className={styles.toolbar}>
            <label className={styles.search}>
              <MagnifyingGlass size={14} aria-hidden="true" />
              <span className={styles.srOnly}>{t('repositories.search')}</span>
              <input
                type="search"
                aria-label={t('repositories.search')}
                placeholder={t('repositories.searchPlaceholder')}
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
            </label>
          </div>
          {visible.length === 0 ? (
            <div className={styles.searchEmpty} role="status">
              <strong>{t('repositories.noMatch')}</strong>
            </div>
          ) : (
            <ul className={styles.list}>
              {visible.map((repository) => (
                <li key={repository.id}>
                  <Link className={styles.row} to={`/repositories/${encodeURIComponent(repository.id)}`} data-testid="repository-row">
                    <span className={styles.mark}><GitBranch size={18} aria-hidden="true" /></span>
                    <span className={styles.rowMain}>
                      <span className={styles.rowTitle}>
                        <strong>{repository.repo_owner_name || repository.name}</strong>
                        <span className={styles.tag}>{serviceProviderLabel(repository)}</span>
                      </span>
                      <span className={styles.rowMeta}>
                        <span className={styles.mono}>{serviceSource(repository)}</span>
                        <span className={styles.created}>{repository.default_branch}</span>
                      </span>
                    </span>
                    <span className={styles.rowSide}>
                      <Gear size={15} aria-label={t('repositories.details')} />
                      <span>{t('repositories.open')}</span>
                      <ArrowRight size={16} aria-hidden="true" />
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </section>
      )}
    </SurfaceInner>
  );
}
