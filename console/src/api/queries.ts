/*
 * queries.ts — TanStack Query hooks over the ApiClient. Query keys are
 * centralised so SSE/status changes can invalidate precisely.
 */
import {
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { useApi } from './ApiProvider';
import type {
  AddMemberInput,
  CreateProjectInput,
  CreateRunInput,
  CreateServiceInput,
  Member,
  Project,
  Run,
  UpdateProjectInput,
} from './types';
import { isTerminal } from './types';

export const qk = {
  me: ['me'] as const,
  projects: ['projects'] as const,
  project: (id: string) => ['project', id] as const,
  runs: (projectId: string) => ['runs', projectId] as const,
  run: (runId: string) => ['run', runId] as const,
  diff: (runId: string) => ['diff', runId] as const,
  pr: (runId: string) => ['pr', runId] as const,
  system: ['system'] as const,
  services: (projectId: string) => ['services', projectId] as const,
  members: (projectId: string) => ['members', projectId] as const,
  users: (q: string) => ['users', q] as const,
};

export function useProjects() {
  const api = useApi();
  return useQuery({ queryKey: qk.projects, queryFn: () => api.listProjects() });
}

export function useProject(id: string) {
  const api = useApi();
  return useQuery({
    queryKey: qk.project(id),
    queryFn: () => api.getProject(id),
    enabled: !!id,
  });
}

export function useCreateProject() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateProjectInput) => api.createProject(input),
    onSuccess: (project: Project) => {
      qc.invalidateQueries({ queryKey: qk.projects });
      qc.setQueryData(qk.project(project.id), project);
    },
  });
}

export function useUpdateProject() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateProjectInput }) =>
      api.updateProject(id, input),
    onSuccess: (project: Project) => {
      qc.setQueryData(qk.project(project.id), project);
      qc.invalidateQueries({ queryKey: qk.projects });
    },
  });
}

export function useDeleteProject() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteProject(id),
    onSuccess: (_void, id: string) => {
      qc.removeQueries({ queryKey: qk.project(id) });
      qc.invalidateQueries({ queryKey: qk.projects });
    },
  });
}

export function useRuns(projectId: string) {
  const api = useApi();
  return useQuery({
    queryKey: qk.runs(projectId),
    queryFn: () => api.listRuns(projectId),
    enabled: !!projectId,
    // Poll the list while any run is non-terminal so badges advance even
    // without a per-run stream open on this page.
    refetchInterval: (query) => {
      const data = query.state.data as Run[] | undefined;
      if (!data) return false;
      return data.some((r) => !isTerminal(r.status)) ? 3000 : false;
    },
  });
}

export function useRun(runId: string, pollWhileNonTerminal = false) {
  const api = useApi();
  return useQuery({
    queryKey: qk.run(runId),
    queryFn: () => api.getRun(runId),
    enabled: !!runId,
    // Polling fallback: when the live SSE stream is unavailable (e.g. a fatal
    // stream error), advance the run status by polling GET /runs/{id} while the
    // run is still non-terminal — mirroring the useRuns list-page pattern so the
    // header still reaches a terminal state without the stream.
    refetchInterval: (query) => {
      if (!pollWhileNonTerminal) return false;
      const data = query.state.data as Run | undefined;
      if (!data) return false;
      return isTerminal(data.status) ? false : 3000;
    },
  });
}

/**
 * Dispatch a run against a specific service (runs are always service-scoped).
 * projectId is only used to invalidate the project's run list afterwards.
 */
export function useCreateServiceRun(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ serviceId, input }: { serviceId: string; input: CreateRunInput }) =>
      api.createServiceRun(serviceId, input),
    onSuccess: (run: Run) => {
      qc.invalidateQueries({ queryKey: qk.runs(projectId) });
      qc.setQueryData(qk.run(run.id), run);
    },
  });
}

/**
 * The Drone-style repo picker listing. Only fires while the add-repository form
 * is open (enabled); a 403 (no linked credential) surfaces as isError and the
 * form falls back to manual URL entry.
 */
export function useProviderRepos(provider: string, q: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: ['provider-repos', provider, q],
    queryFn: () => api.listProviderRepos(provider, q),
    enabled: enabled && !!provider,
    staleTime: 30_000,
    retry: false,
  });
}

/** Add a repository (service) to a project. Refreshes the project + its services. */
export function useCreateService(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateServiceInput) => api.createService(projectId, input),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.services(projectId) });
      qc.invalidateQueries({ queryKey: qk.project(projectId) });
    },
  });
}

export function useCancelRun() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (runId: string) => api.cancelRun(runId),
    onSuccess: (run: Run) => {
      qc.setQueryData(qk.run(run.id), run);
      qc.invalidateQueries({ queryKey: qk.runs(run.project_id) });
    },
  });
}

export function useRetryRun() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (runId: string) => api.retryRun(runId),
    onSuccess: (run: Run) => {
      qc.setQueryData(qk.run(run.id), run);
      qc.invalidateQueries({ queryKey: qk.runs(run.project_id) });
    },
  });
}

export function useDiff(runId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: qk.diff(runId),
    queryFn: () => api.getDiff(runId),
    enabled: enabled && !!runId,
    retry: false,
  });
}

/**
 * The run's PR view (link, live state, review runs). Enabled only when the PR
 * tab is open. Refetches on a modest interval so a newly-requested review's
 * status (and a merge/close on the provider) surfaces without a manual reload.
 */
export function usePR(runId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: qk.pr(runId),
    queryFn: () => api.getPR(runId),
    enabled: enabled && !!runId,
    refetchInterval: enabled ? 5000 : false,
  });
}

/**
 * Request an AI review of a run's PR. On success the caller navigates to the new
 * review run; we also refresh the PR view so the reviews list picks up the new
 * (queued) run.
 */
export function useRequestReview() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (runId: string) => api.requestReview(runId),
    onSuccess: (run: Run, runId: string) => {
      qc.setQueryData(qk.run(run.id), run);
      qc.invalidateQueries({ queryKey: qk.pr(runId) });
      qc.invalidateQueries({ queryKey: qk.runs(run.project_id) });
    },
  });
}

/**
 * The cluster-admin system snapshot. Capacity counts drift as runs start/finish,
 * so refresh on a modest interval to keep the Cluster view live-ish without a
 * stream. `enabled` gates the fetch to cluster-admins — a project-admin who
 * lands on /system never issues the request (the gate is honest, not just visual).
 */
export function useSystem(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.system,
    queryFn: () => api.getSystem(),
    enabled,
    refetchInterval: 5000,
  });
}

/* ---- members (blueprint §2) ---------------------------------------------- */

export function useMembers(projectId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.members(projectId),
    queryFn: () => api.listMembers(projectId),
    enabled: enabled && !!projectId,
  });
}

export function useAddMember(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: AddMemberInput) => api.addMember(projectId, input),
    onSuccess: (member: Member) => {
      qc.setQueryData<Member[]>(qk.members(projectId), (prev) => {
        const list = prev ? [...prev] : [];
        const i = list.findIndex((m) => m.user_id === member.user_id);
        if (i >= 0) list[i] = member;
        else list.push(member);
        return list;
      });
      qc.invalidateQueries({ queryKey: qk.members(projectId) });
    },
  });
}

export function useRemoveMember(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) => api.removeMember(projectId, userId),
    onSuccess: (_void, userId: string) => {
      qc.setQueryData<Member[]>(qk.members(projectId), (prev) =>
        prev ? prev.filter((m) => m.user_id !== userId) : prev,
      );
      qc.invalidateQueries({ queryKey: qk.members(projectId) });
    },
  });
}

/**
 * User search for the add-member picker. Debounce-friendly: pass the live query
 * string; the query only fires once it is non-empty so an empty box is quiet.
 */
export function useSearchUsers(q: string) {
  const api = useApi();
  return useQuery({
    queryKey: qk.users(q),
    queryFn: () => api.searchUsers(q),
    enabled: q.trim().length > 0,
  });
}
