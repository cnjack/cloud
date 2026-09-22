import { useQueries } from '@tanstack/react-query';
import { useApi } from '../api/ApiProvider';
import { qk, useRepositories } from '../api/queries';

/** Every account surface shares the same authorized repository/task navigation. */
export function useAccountNavigation() {
  const api = useApi();
  const repositories = useRepositories();
  const projects = [...new Set((repositories.data ?? []).map(repo => repo.project_id))];
  const conversations = useQueries({ queries: projects.map(id => ({
    queryKey: qk.runs(id), queryFn: () => api.listRuns(id), staleTime: 15_000, refetchInterval: 30_000,
  })) });
  return {
    repositories: repositories.data ?? [],
    runs: conversations.flatMap(query => query.data ?? []),
    isLoading: repositories.isLoading || conversations.some(query => query.isLoading),
    error: repositories.error ?? conversations.find(query => query.isError)?.error,
    onRetry: () => { void repositories.refetch(); conversations.forEach(query => { if (query.isError) void query.refetch(); }); },
  };
}
