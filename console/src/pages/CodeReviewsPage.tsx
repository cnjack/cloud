import { useMemo, useState } from 'react';
import { useQueries } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useApi } from '../api/ApiProvider';
import { ApiError } from '../api/client';
import type { Project, Run, Service } from '../api/types';
import { qk, useProjects, useRequestReview } from '../api/queries';
import { Button } from '../components/Button';
import { useModelGate } from '../components/ModelGate';
import { PageHeader, SurfaceInner } from '../components/PageLayout';
import { useToast } from '../components/Toast';
import { serviceRepoLabel } from '../lib/repo';
import { RunActivityList } from '../project-workspace/RunActivityList';
import styles from './CodeReviewsPage.module.css';

interface RepositoryOption {
  project: Project;
  repository: Service;
}

interface ReviewSource {
  repository: RepositoryOption;
  run: Run;
}

function sourceRepository(run: Run, repositories: RepositoryOption[]) {
  if (run.service_id) return repositories.find((item) => item.repository.id === run.service_id);
  const inProject = repositories.filter((item) => item.project.id === run.project_id);
  return inProject.length === 1 ? inProject[0] : undefined;
}

function reviewSourceLabel(run: Run) {
  const number = run.pr_number ? `#${run.pr_number}` : 'PR';
  return `${number} · ${run.pr_title || run.prompt}`;
}

export function CodeReviewsPage() {
  const { t } = useTranslation();
  const api = useApi();
  const navigate = useNavigate();
  const toast = useToast();
  const [searchParams] = useSearchParams();
  const requestedRepositoryId = searchParams.get('repository') ?? '';
  const [repositoryChoice, setRepositoryChoice] = useState('');
  const [sourceChoice, setSourceChoice] = useState('');
  const projects = useProjects();
  const runQueries = useQueries({
    queries: (projects.data ?? []).map((project) => ({
      queryKey: qk.runs(project.id),
      queryFn: () => api.listRuns(project.id),
      staleTime: 15_000,
    })),
  });
  const allRuns = useMemo(() => runQueries.flatMap((query) => query.data ?? []), [runQueries]);
  const reviews = useMemo(() => allRuns
    .filter((run) => run.kind === 'review')
    .sort((a, b) => b.created_at.localeCompare(a.created_at)), [allRuns]);
  const repositories = useMemo<RepositoryOption[]>(() => (projects.data ?? []).flatMap((project) =>
    (project.services ?? []).map((repository) => ({ project, repository }))), [projects.data]);
  const reviewSources = useMemo<ReviewSource[]>(() => allRuns
    .filter((run) => run.kind !== 'review' && run.status === 'succeeded' && !!run.pr_url)
    .flatMap((run) => {
      const repository = sourceRepository(run, repositories);
      return repository ? [{ repository, run }] : [];
    })
    .sort((a, b) => b.run.created_at.localeCompare(a.run.created_at)), [allRuns, repositories]);
  const requestedRepositoryExists = repositories.some(({ repository }) => repository.id === requestedRepositoryId);
  const selectedRepositoryId = repositories.some(({ repository }) => repository.id === repositoryChoice)
    ? repositoryChoice
    : requestedRepositoryExists
      ? requestedRepositoryId
      : repositories[0]?.repository.id ?? '';
  const selectedRepository = repositories.find(({ repository }) => repository.id === selectedRepositoryId);
  const selectedSources = reviewSources.filter(({ repository }) => repository.repository.id === selectedRepositoryId);
  const selectedSourceId = selectedSources.some(({ run }) => run.id === sourceChoice)
    ? sourceChoice
    : selectedSources[0]?.run.id ?? '';
  const selectedSource = selectedSources.find(({ run }) => run.id === selectedSourceId);
  const requestReview = useRequestReview();
  const modelGate = useModelGate(selectedRepository?.project.id ?? '', !!selectedSource);
  const error = projects.error ?? runQueries.find((query) => query.error)?.error;
  const loading = projects.isLoading || runQueries.some((query) => query.isLoading);

  const createReview = () => {
    if (!selectedSource) return;
    requestReview.mutate(selectedSource.run.id, {
      onSuccess: (run) => {
        toast.push({ kind: 'success', message: t('codeReviewsPage.createSuccess') });
        navigate(`/runs/${run.id}`);
      },
      onError: (requestError) => toast.push({
        kind: 'error',
        message: requestError instanceof ApiError
          ? requestError.message
          : t('codeReviewsPage.createError'),
      }),
    });
  };

  return (
    <SurfaceInner>
      <PageHeader
        eyebrow={t('codeReviewsPage.eyebrow')}
        title={t('codeReviewsPage.title')}
        description={t('codeReviewsPage.description')}
      />
      <section className={styles.create} aria-labelledby="create-review-heading">
        <header>
          <div>
            <span>{t('codeReviewsPage.createEyebrow')}</span>
            <h2 id="create-review-heading">{t('codeReviewsPage.createTitle')}</h2>
            <p>{t('codeReviewsPage.createDescription')}</p>
          </div>
        </header>
        {loading ? (
          <p className={styles.status}>{t('codeReviewsPage.loadingSources')}</p>
        ) : error ? (
          <div className={styles.blocked} role="alert">
            <strong>{t('codeReviewsPage.sourcesError')}</strong>
            <Button variant="secondary" onClick={() => {
              void projects.refetch();
              for (const query of runQueries) void query.refetch();
            }}>{t('common.retry')}</Button>
          </div>
        ) : repositories.length === 0 ? (
          <div className={styles.blocked} role="status">
            <strong>{t('codeReviewsPage.noReviewablePr')}</strong>
            <span>{t('codeReviewsPage.noReviewablePrDescription')}</span>
            <Link to="/">{t('codeReviewsPage.openWorkHome')}</Link>
          </div>
        ) : (
          <div className={styles.form}>
            <label>
              <span>{t('codeReviewsPage.repositoryLabel')}</span>
              <select
                aria-label={t('codeReviewsPage.repositoryLabel')}
                value={selectedRepositoryId}
                onChange={(event) => {
                  setRepositoryChoice(event.target.value);
                  setSourceChoice('');
                }}
              >
                {repositories.map(({ repository }) => (
                  <option key={repository.id} value={repository.id}>{serviceRepoLabel(repository)}</option>
                ))}
              </select>
            </label>
            {selectedSources.length === 0 ? (
              <div className={styles.blocked} role="status">
                <strong>{t('codeReviewsPage.noReviewablePr')}</strong>
                <span>{t('codeReviewsPage.noReviewablePrDescription')}</span>
                <Link to={`/?repository=${encodeURIComponent(selectedRepositoryId)}`}>
                  {t('codeReviewsPage.openWorkHome')}
                </Link>
              </div>
            ) : (
              <>
                <label className={styles.prField}>
                  <span>{t('codeReviewsPage.pullRequestLabel')}</span>
                  <select
                    aria-label={t('codeReviewsPage.pullRequestLabel')}
                    value={selectedSourceId}
                    onChange={(event) => setSourceChoice(event.target.value)}
                  >
                    {selectedSources.map(({ run }) => (
                      <option key={run.id} value={run.id}>{reviewSourceLabel(run)}</option>
                    ))}
                  </select>
                </label>
                <div className={styles.action}>
                  <Button
                    variant="primary"
                    onClick={createReview}
                    loading={requestReview.isPending}
                    disabled={!modelGate.configured}
                  >
                    {t('codeReviewsPage.createAction')}
                  </Button>
                </div>
                {selectedSource?.run.pr_url && (
                  <a className={styles.prLink} href={selectedSource.run.pr_url} target="_blank" rel="noreferrer">
                    {t('codeReviewsPage.openPullRequest')}
                  </a>
                )}
                {modelGate.notice}
              </>
            )}
          </div>
        )}
      </section>
      <RunActivityList
        runs={reviews}
        isLoading={loading}
        error={error}
        onRetry={() => {
          void projects.refetch();
          for (const query of runQueries) void query.refetch();
        }}
        filter="reviews"
        onFilterChange={() => {}}
        canRun
        showFilters={false}
        emptyTitle={t('codeReviewsPage.emptyTitle')}
        emptyDescription={t('codeReviewsPage.emptyDescription')}
      />
    </SurfaceInner>
  );
}
