import { useMemo } from 'react';
import { useQueries } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useApi } from '../api/ApiProvider';
import { qk, useProjects } from '../api/queries';
import { PageHeader, SurfaceInner } from '../components/PageLayout';
import { RunActivityList } from '../project-workspace/RunActivityList';

export function CodeReviewsPage() {
  const { t } = useTranslation();
  const api = useApi();
  const projects = useProjects();
  const runQueries = useQueries({
    queries: (projects.data ?? []).map((project) => ({
      queryKey: qk.runs(project.id),
      queryFn: () => api.listRuns(project.id),
      staleTime: 15_000,
    })),
  });
  const reviews = useMemo(() => runQueries
    .flatMap((query) => query.data ?? [])
    .filter((run) => run.kind === 'review')
    .sort((a, b) => b.created_at.localeCompare(a.created_at)), [runQueries]);
  const error = projects.error ?? runQueries.find((query) => query.error)?.error;
  const loading = projects.isLoading || runQueries.some((query) => query.isLoading);

  return (
    <SurfaceInner>
      <PageHeader
        eyebrow={t('codeReviewsPage.eyebrow')}
        title={t('codeReviewsPage.title')}
        description={t('codeReviewsPage.description')}
      />
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
