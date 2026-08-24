/*
 * queries.ts — TanStack Query hooks over the ApiClient. Query keys are
 * centralised so SSE/status changes can invalidate precisely.
 */
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { useApi } from './ApiProvider';
import type {
  AddMemberInput,
  AutomationExecution,
  CreateProjectAutomationInput,
  CreateApiKeyInput,
  CreateApiKeyResponse,
  CreateModelInput,
  CreateModelProviderInput,
  CreateProviderModelInput,
  CreateProjectInput,
  CreateRunInput,
  CreateServiceInput,
	ServiceBranch,
  KanbanCardExecution,
  Member,
  Model,
  Project,
  PluginConsentInput,
  PluginAuditEvent,
  ProviderKind,
  UpdateClusterProviderConfigInput,
  UpdateProjectAutomationInput,
  RetryRunOptions,
  ResumeSessionOptions,
  Run,
  UpdateModelInput,
  UpdateModelProviderInput,
  UpdateProjectInput,
  UpdateProviderModelInput,
  UpdateServiceInput,
  CreateModelPricingRevisionInput,
} from './types';
import { isTerminal } from './types';
import { reconcileRunSnapshot } from './runCache';

export function kanbanCardExecutionPollInterval(
  items: readonly KanbanCardExecution[] | undefined,
): number | false {
  if (!items || items.length === 0) return 5_000;
  return items.some((item) =>
    item.status === 'received' || item.status === 'blocked' ||
    item.status === 'queued' || item.status === 'running')
    ? 5_000 : false;
}

export function automationExecutionPollInterval(
  items: readonly AutomationExecution[] | undefined,
): number | false {
  return items?.some((item) =>
    item.state === 'accepted' || item.state === 'queued' || item.state === 'running' ||
    (item.state === 'blocked' && item.output_mode === 'create_card'))
    ? 5_000 : false;
}

export const qk = {
  me: ['me'] as const,
  projects: ['projects'] as const,
  repositories: ['repositories'] as const,
  repository: (id: string) => ['repository', id] as const,
  project: (id: string) => ['project', id] as const,
  runs: (projectId: string) => ['runs', projectId] as const,
  run: (runId: string) => ['run', runId] as const,
  diff: (runId: string) => ['diff', runId] as const,
  pr: (runId: string) => ['pr', runId] as const,
  system: ['system'] as const,
  models: ['models'] as const,
  accountModels: ['account-models'] as const,
  modelProviders: ['model-providers'] as const,
  modelProviderCatalog: (id: string) => ['model-provider-catalog', id] as const,
  projectModelProviders: (projectId: string) => ['project-model-providers', projectId] as const,
  projectModelProviderCatalog: (projectId: string, providerId: string) =>
    ['project-model-provider-catalog', projectId, providerId] as const,
  projectModels: (projectId: string) => ['project-models', projectId] as const,
  // D31: the member+ reduced board-link list that gates the "Kanban" header
  // button + feeds the embed modal's selector.
  projectBoardLinks: (projectId: string) => ['project-board-links', projectId] as const,
  serviceKanban: (serviceId: string) => ['service-kanban', serviceId] as const,
  serviceKanbanPolicy: (serviceId: string) => ['service-kanban-policy', serviceId] as const,
  serviceKanbanCardExecutions: (serviceId: string, workspaceId: string, documentPath: string) =>
    ['service-kanban-card-executions', serviceId, workspaceId, documentPath] as const,
  serviceBranches: (serviceId: string) => ['service-branches', serviceId] as const,
  projectPlugins: (projectId: string) => ['project-plugins', projectId] as const,
  projectPluginImpact: (projectId: string, installationId: string) =>
    ['project-plugin-impact', projectId, installationId] as const,
  projectPluginAudit: (projectId: string, installationId: string) =>
    ['project-plugin-audit', projectId, installationId] as const,
  pluginRepositories: (projectId: string, installationId: string, q: string) =>
    ['plugin-repositories', projectId, installationId, q] as const,
  pluginWorkspaces: (projectId: string, installationId: string) =>
    ['plugin-workspaces', projectId, installationId] as const,
  pluginBoards: (projectId: string, installationId: string, workspaceId: string) =>
    ['plugin-boards', projectId, installationId, workspaceId] as const,
  projectAutomations: (projectId: string) => ['project-automations', projectId] as const,
  projectAutomation: (projectId: string, automationId: string) =>
    ['project-automation', projectId, automationId] as const,
  automationExecutions: (automationId: string, state: string) =>
    ['automation-executions', automationId, state] as const,
  automationUsage: (automationId: string, from: string, to: string) =>
    ['automation-usage', automationId, from, to] as const,
  projectUsage: (projectId: string, groupBy: string, from: string, to: string) =>
    ['project-usage', projectId, groupBy, from, to] as const,
  serviceUsage: (serviceId: string, from: string, to: string) =>
    ['repository-usage', serviceId, from, to] as const,
  accountUsage: (groupBy: string, from: string, to: string) =>
    ['account-usage', groupBy, from, to] as const,
  providerCapabilities: (provider: ProviderKind) => ['provider-capabilities', provider] as const,
  modelPricingRevisions: (modelId: string) => ['model-pricing-revisions', modelId] as const,
  githubAppInstallations: (projectId: string) => ['github-app-installations', projectId] as const,
  jtypePluginConnect: (projectId: string, installationId: string, connectId: string) =>
    ['jtype-plugin-connect', projectId, installationId, connectId] as const,
  clusterProvider: (provider: ProviderKind) => ['cluster-provider', provider] as const,
  apiKeys: (projectId: string) => ['api-keys', projectId] as const,
  services: (projectId: string) => ['services', projectId] as const,
  members: (projectId: string) => ['members', projectId] as const,
  users: (q: string) => ['users', q] as const,
};

export function useProjects(enabled = true) {
  const api = useApi();
  return useQuery({ queryKey: qk.projects, queryFn: () => api.listProjects(), enabled });
}

export function useProject(id: string) {
  const api = useApi();
  return useQuery({
    queryKey: qk.project(id),
    queryFn: () => api.getProject(id),
    enabled: !!id,
  });
}

export function useRepositories(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.repositories,
    queryFn: () => api.listRepositories(),
    enabled,
    staleTime: 15_000,
  });
}

export function useRepository(id: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.repository(id),
    queryFn: () => api.getRepository(id),
    enabled: enabled && !!id,
    staleTime: 15_000,
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
  const qc = useQueryClient();
  return useQuery({
    queryKey: qk.run(runId),
    queryFn: async () => {
      const incoming = await api.getRun(runId);
      return reconcileRunSnapshot(
        qc.getQueryData<Run>(qk.run(runId)),
        incoming,
      );
    },
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

/**
 * Branches are resolved from the selected Service's repository binding. The
 * provider error is intentionally surfaced to the composer rather than falling
 * back to an unverified arbitrary branch list.
 */
export function useServiceBranches(serviceId: string, enabled: boolean) {
	const api = useApi();
	return useQuery<ServiceBranch[]>({
		queryKey: qk.serviceBranches(serviceId),
		queryFn: () => api.listServiceBranches(serviceId),
		enabled: enabled && !!serviceId,
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
    mutationFn: ({ runId, options }: { runId: string; options?: RetryRunOptions }) => api.retryRun(runId, options),
    onSuccess: (run: Run) => {
      qc.setQueryData(qk.run(run.id), run);
      qc.invalidateQueries({ queryKey: qk.runs(run.project_id) });
    },
  });
}

/**
 * Continue a finished session run in a NEW run that reloads the same ACP session
 * (F9b / D23 ①②). On success the caller navigates to the new run; we also seed
 * its cache entry and refresh the project's run list.
 */
export function useResumeSession() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ runId, prompt, options }: { runId: string; prompt: string; options?: ResumeSessionOptions }) =>
      api.resumeSession(runId, prompt, options),
    onSuccess: (run: Run) => {
      qc.setQueryData(qk.run(run.id), run);
      qc.invalidateQueries({ queryKey: qk.runs(run.project_id) });
    },
  });
}

/* ---- multi-turn session (D22) --------------------------------------------- */

/**
 * Feed a follow-up prompt to a session run. The message shows in the timeline
 * via its user.message SSE event; the run refetch picks up the status flip
 * (awaiting_input → running) once the runner claims the message.
 */
export function useSendMessage() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ runId, prompt }: { runId: string; prompt: string }) =>
      api.sendMessage(runId, prompt),
    onSuccess: (_msg, { runId }) => {
      qc.invalidateQueries({ queryKey: qk.run(runId) });
    },
  });
}

/** Wind a session down (POST /runs/{id}/finish). Idempotent server-side. */
export function useFinishSession() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (runId: string) => api.finishSession(runId),
    onSuccess: (run: Run) => {
      qc.setQueryData(qk.run(run.id), run);
      qc.invalidateQueries({ queryKey: qk.runs(run.project_id) });
    },
  });
}

/**
 * Answer a pending permission request of an approval-mode session (F8b,
 * POST /runs/{id}/permission-response). No cache invalidation needed: the
 * card's resolved state arrives on the event stream
 * (agent.permission_resolved); RunDetailPage keeps the optimistic
 * "decided, waiting" state itself.
 */
export function useRespondPermission() {
  const api = useApi();
  return useMutation({
    mutationFn: ({
      runId,
      requestId,
      optionId,
    }: {
      runId: string;
      requestId: string;
      optionId: string;
    }) => api.respondPermission(runId, requestId, optionId),
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

/**
 * The Cluster page "sync runner image" action: (re)assert the runner-image
 * prewarm DaemonSet so every node re-pulls the current image (including a
 * re-pushed :latest). The /system snapshot is invalidated so the runner panel
 * shows the post-sync desired/ready counts + last_sync.
 */
export function usePrewarmRunnerImage() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.prewarmRunnerImage(),
    onSettled: () => qc.invalidateQueries({ queryKey: qk.system }),
  });
}

/* ---- model catalog + project grants (D21) -------------------------------- */

/** The whole model catalog (cluster-admin). Powers the Cluster page ModelCard. */
export function useModels(enabled = true) {
  const api = useApi();
  return useQuery({ queryKey: qk.models, queryFn: () => api.listModels(), enabled });
}

/** Direct Account grants used by headless Repository Agent Workflows. */
export function useAccountModels(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.accountModels,
    queryFn: () => api.listAccountModels(),
    enabled,
    staleTime: 15_000,
    refetchInterval: 30_000,
  });
}

export function useModelProviders(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.modelProviders,
    queryFn: () => api.listModelProviders(),
    enabled,
  });
}

export function useCreateModelProvider() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateModelProviderInput) => api.createModelProvider(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.modelProviders }),
  });
}

export function useUpdateModelProvider() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateModelProviderInput }) =>
      api.updateModelProvider(id, input),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.modelProviders });
      qc.invalidateQueries({ queryKey: qk.models });
      qc.invalidateQueries({ queryKey: ['project-models'] });
    },
  });
}

export function useDeleteModelProvider() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteModelProvider(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.modelProviders });
      qc.invalidateQueries({ queryKey: qk.models });
      qc.invalidateQueries({ queryKey: ['project-models'] });
    },
  });
}

export function useVerifyModelProvider() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.verifyModelProvider(id),
    // The backend persists both successful probes and visible failure details.
    // Refresh after either outcome so a failed test appears on the provider card
    // immediately instead of only after a manual page reload.
    onSettled: () => qc.invalidateQueries({ queryKey: qk.modelProviders }),
  });
}

export function useModelProviderCatalog(providerId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: qk.modelProviderCatalog(providerId),
    queryFn: () => api.getModelProviderCatalog(providerId),
    enabled: enabled && !!providerId,
    retry: false,
  });
}

export function useCreateProviderModel() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ providerId, input }: { providerId: string; input: CreateProviderModelInput }) =>
      api.createProviderModel(providerId, input),
    onSuccess: (_model, { providerId }) => {
      qc.invalidateQueries({ queryKey: qk.modelProviders });
      qc.invalidateQueries({ queryKey: qk.modelProviderCatalog(providerId) });
      qc.invalidateQueries({ queryKey: qk.models });
    },
  });
}

/* ---- project-owned model providers (M2) ---------------------------------- */

/**
 * Every project-owned provider/model mutation makes three views stale: the
 * project's provider list (this admin surface), the project's usable-model union
 * (`useProjectModels`, keyed off qk.projectModels — feeds the composer picker,
 * the per-service default `<Select>`, and the ModelGate), and — because a
 * just-created enabled model must reach any OTHER open project view — the whole
 * `['project-models']` prefix. Mirrors the useSetModelGrant invalidation set.
 */
function invalidateProjectModels(
  qc: ReturnType<typeof useQueryClient>,
  projectId: string,
): void {
  qc.invalidateQueries({ queryKey: qk.projectModelProviders(projectId) });
  qc.invalidateQueries({ queryKey: qk.projectModels(projectId) });
  qc.invalidateQueries({ queryKey: ['project-models'] });
}

/** A project's own model providers (owner write / member read). */
export function useProjectModelProviders(projectId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.projectModelProviders(projectId),
    queryFn: () => api.listProjectModelProviders(projectId),
    enabled: enabled && !!projectId,
  });
}

export function useCreateProjectModelProvider(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateModelProviderInput) => api.createProjectModelProvider(projectId, input),
    onSuccess: () => invalidateProjectModels(qc, projectId),
  });
}

export function useUpdateProjectModelProvider(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateModelProviderInput }) =>
      api.updateProjectModelProvider(projectId, id, input),
    onSuccess: () => invalidateProjectModels(qc, projectId),
  });
}

export function useDeleteProjectModelProvider(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteProjectModelProvider(projectId, id),
    onSuccess: () => invalidateProjectModels(qc, projectId),
  });
}

export function useVerifyProjectModelProvider(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.verifyProjectModelProvider(projectId, id),
    // Persisted probe result (success or visible failure) → refresh the card.
    onSettled: () => qc.invalidateQueries({ queryKey: qk.projectModelProviders(projectId) }),
  });
}

export function useProjectModelProviderCatalog(projectId: string, providerId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: qk.projectModelProviderCatalog(projectId, providerId),
    queryFn: () => api.getProjectModelProviderCatalog(projectId, providerId),
    enabled: enabled && !!projectId && !!providerId,
    retry: false,
  });
}

export function useCreateProjectProviderModel(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ providerId, input }: { providerId: string; input: CreateProviderModelInput }) =>
      api.createProjectProviderModel(projectId, providerId, input),
    onSuccess: (_model, { providerId }) => {
      invalidateProjectModels(qc, projectId);
      qc.invalidateQueries({ queryKey: qk.projectModelProviderCatalog(projectId, providerId) });
    },
  });
}

export function useUpdateProjectProviderModel(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ providerId, modelId, input }: { providerId: string; modelId: string; input: UpdateProviderModelInput }) =>
      api.updateProjectProviderModel(projectId, providerId, modelId, input),
    onSuccess: () => invalidateProjectModels(qc, projectId),
  });
}

export function useDeleteProjectProviderModel(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ providerId, modelId }: { providerId: string; modelId: string }) =>
      api.deleteProjectProviderModel(projectId, providerId, modelId),
    onSuccess: () => invalidateProjectModels(qc, projectId),
  });
}

/**
 * The models granted to a project (member+). Its length + env_fallback drive the
 * ModelGate's `configured` signal AND the composer's model select. Kept fresh on
 * a modest interval so a just-granted model reaches an open composer.
 */
export function useProjectModels(projectId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.projectModels(projectId),
    queryFn: () => api.listProjectModels(projectId),
    enabled: enabled && !!projectId,
    refetchInterval: 15000,
  });
}

export function useCreateModel() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateModelInput) => api.createModel(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.models }),
  });
}

export function useUpdateModel() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdateModelInput }) =>
      api.updateModel(id, input),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.models });
      qc.invalidateQueries({ queryKey: qk.modelProviders });
      qc.invalidateQueries({ queryKey: ['project-models'] });
    },
  });
}

export function useDeleteModel() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteModel(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.models });
      // Grants may have changed → any open composer's model list is now stale.
      qc.invalidateQueries({ queryKey: ['project-models'] });
    },
  });
}

/** Grant or revoke a project's authorization for a model (cluster-admin). */
export function useSetModelGrant() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ modelId, projectId, granted }: { modelId: string; projectId: string; granted: boolean }) =>
      granted ? api.grantModel(modelId, projectId) : api.revokeModel(modelId, projectId),
    onSuccess: (_model: Model, { projectId }) => {
      qc.invalidateQueries({ queryKey: qk.models });
      qc.invalidateQueries({ queryKey: qk.modelProviders });
      qc.invalidateQueries({ queryKey: qk.projectModels(projectId) });
    },
  });
}

/** Grant or revoke Account-wide Desktop access to a Cluster-global model. */
export function useSetModelAccountGrant() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ modelId, userId, granted }: { modelId: string; userId: string; granted: boolean }) =>
      granted ? api.grantModelToAccount(modelId, userId) : api.revokeModelFromAccount(modelId, userId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.models });
      qc.invalidateQueries({ queryKey: qk.modelProviders });
    },
  });
}

/** Edit a service (owner) — currently just its default model (D21). */
export function useUpdateService(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ serviceId, input }: { serviceId: string; input: UpdateServiceInput }) =>
      api.updateService(serviceId, input),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.services(projectId) });
      qc.invalidateQueries({ queryKey: qk.project(projectId) });
    },
  });
}

/** Stop all work, cascade-delete a service, and refresh every project surface. */
export function useDeleteService(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (serviceId: string) => api.deleteService(serviceId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.services(projectId) });
      qc.invalidateQueries({ queryKey: qk.project(projectId) });
      qc.invalidateQueries({ queryKey: qk.projects });
    },
  });
}

/**
 * D31: the member+ board-embed link list — gates the project header's "Kanban"
 * button and populates the modal's link selector. It is member+ and returns no
 * credential fields, so a viewer / non-member gets a 403 → empty data → no button.
 *
 * `retry: false` so a 403/409/503 surfaces at once (no button) instead of
 * spinning through retries — fail-visible, and the button simply stays hidden.
 */
export function useProjectBoardLinks(projectId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.projectBoardLinks(projectId),
    queryFn: () => api.listProjectBoardLinks(projectId),
    enabled: enabled && !!projectId,
    retry: false,
  });
}

export function usePutServiceKanban(projectId: string, serviceId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: import('./types').PutServiceKanbanInput) => api.putServiceKanban(serviceId, input),
    onSuccess: async () => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: qk.projectBoardLinks(projectId) }),
        qc.invalidateQueries({ queryKey: qk.projectAutomations(projectId) }),
        qc.invalidateQueries({ queryKey: qk.serviceKanban(serviceId) }),
      ]);
    },
  });
}

export function useServiceKanban(serviceId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.serviceKanban(serviceId),
    queryFn: () => api.getServiceKanban(serviceId),
    enabled: enabled && !!serviceId,
    retry: false,
    staleTime: 15_000,
  });
}

export function useServiceKanbanPolicy(serviceId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.serviceKanbanPolicy(serviceId),
    queryFn: () => api.getServiceKanbanPolicy(serviceId),
    enabled: enabled && !!serviceId,
    retry: false,
  });
}

export function useServiceKanbanCardExecutions(
  serviceId: string,
  workspaceId: string,
  documentPath: string,
  enabled = true,
) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: qk.serviceKanbanCardExecutions(serviceId, workspaceId, documentPath),
    queryFn: ({ pageParam }) => api.listServiceKanbanCardExecutions(
      serviceId, workspaceId, documentPath, pageParam ?? undefined,
    ),
    initialPageParam: null as string | null,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
    enabled: enabled && !!serviceId && !!workspaceId && !!documentPath,
    retry: false,
    refetchInterval: (query) => {
      const items = query.state.data?.pages.flatMap((page) => page.items);
      // Keep polling an empty Card so a receipt appears without closing and
      // reopening the detail after the user moves it into the trigger column.
      // Stop only once every known occurrence is terminal.
      return kanbanCardExecutionPollInterval(items);
    },
  });
}

export function useCreateServiceKanbanOccurrence(
  serviceId: string,
  workspaceId: string,
  documentPath: string,
) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (idempotencyKey: string) => api.createServiceKanbanOccurrence(serviceId, {
      workspace_id: workspaceId,
      document_path: documentPath,
      idempotency_key: idempotencyKey,
    }),
    onSuccess: () => qc.invalidateQueries({
      queryKey: qk.serviceKanbanCardExecutions(serviceId, workspaceId, documentPath),
    }),
  });
}

export function useDeleteServiceKanban(projectId: string, serviceId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.deleteServiceKanban(serviceId),
    onSuccess: async () => {
      await Promise.all([
        qc.invalidateQueries({ queryKey: qk.projectBoardLinks(projectId) }),
        qc.invalidateQueries({ queryKey: qk.projectAutomations(projectId) }),
        qc.invalidateQueries({ queryKey: qk.serviceKanban(serviceId) }),
      ]);
    },
  });
}

/* ---- unified Project Plugins and Automations ---------------------------- */

export function useProjectPlugins(projectId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.projectPlugins(projectId),
    queryFn: () => api.listProjectPlugins(projectId),
    enabled: enabled && !!projectId,
  });
}

export function useStartPluginInstall(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ provider, input }: { provider: ProviderKind; input: PluginConsentInput }) =>
      api.startPluginInstall(projectId, provider, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.projectPlugins(projectId) }),
  });
}

export function useGitHubAppInstallations(projectId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.githubAppInstallations(projectId),
    queryFn: () => api.listGitHubAppInstallations(projectId),
    enabled: enabled && !!projectId,
    retry: false,
  });
}

export function useSelectGitHubAppInstallation(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ installationId, input }: { installationId: string; input: PluginConsentInput }) =>
      api.selectGitHubAppInstallation(projectId, installationId, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.projectPlugins(projectId) }),
  });
}

export function usePreviewGitHubAppInstallationConsent(projectId: string) {
  const api = useApi();
  return useMutation({
    mutationFn: (installationId: string) =>
      api.previewGitHubAppInstallationConsent(projectId, installationId),
  });
}

export function useProjectPluginAudit(projectId: string, installationId: string, enabled = true) {
  const api = useApi();
  return useQuery<PluginAuditEvent[]>({
    queryKey: qk.projectPluginAudit(projectId, installationId),
    queryFn: () => api.listProjectPluginAudit(projectId, installationId),
    enabled: enabled && !!projectId && !!installationId,
    retry: false,
  });
}

export function useJTypePluginConnectStatus(
  projectId: string,
  installationId: string,
  connectId: string,
  enabled = true,
) {
  const api = useApi();
  return useQuery({
    queryKey: qk.jtypePluginConnect(projectId, installationId, connectId),
    queryFn: () => api.getJTypePluginConnectStatus(projectId, installationId, connectId),
    enabled: enabled && !!projectId && !!installationId && !!connectId,
    refetchInterval: (query) => query.state.data?.status === 'complete' ? false : 2000,
    retry: false,
  });
}

export function useSetProjectPluginEnabled(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ installationId, enabled }: { installationId: string; enabled: boolean }) =>
      api.setProjectPluginEnabled(installationId, enabled),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.projectPlugins(projectId) });
    },
  });
}

export function useSetProjectPluginWorkspace(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ installationId, workspaceId }: { installationId: string; workspaceId: string }) =>
      api.setProjectPluginWorkspace(installationId, workspaceId),
    onSuccess: (_plugin, { installationId }) => {
      qc.invalidateQueries({ queryKey: qk.projectPlugins(projectId) });
      qc.invalidateQueries({ queryKey: qk.pluginWorkspaces(projectId, installationId) });
    },
  });
}

export function useUninstallProjectPlugin(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ installationId, force = false }: { installationId: string; force?: boolean }) =>
      api.uninstallProjectPlugin(installationId, force),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.projectPlugins(projectId) }),
  });
}

export function useProjectPluginImpact(projectId: string, installationId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.projectPluginImpact(projectId, installationId),
    queryFn: () => api.getProjectPluginImpact(projectId, installationId),
    enabled: enabled && !!projectId && !!installationId,
    retry: false,
  });
}

export function usePluginRepositories(projectId: string, installationId: string, q = '', enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.pluginRepositories(projectId, installationId, q),
    queryFn: () => api.listPluginRepositories(projectId, installationId, q || undefined),
    enabled: enabled && !!projectId && !!installationId,
    retry: false,
  });
}

export function usePluginWorkspaces(projectId: string, installationId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.pluginWorkspaces(projectId, installationId),
    queryFn: () => api.listPluginWorkspaces(projectId, installationId),
    enabled: enabled && !!projectId && !!installationId,
    retry: false,
  });
}

export function usePluginBoards(projectId: string, installationId: string, workspaceId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.pluginBoards(projectId, installationId, workspaceId),
    queryFn: () => api.listPluginBoards(projectId, installationId, workspaceId || undefined),
    enabled: enabled && !!projectId && !!installationId && !!workspaceId,
    retry: false,
  });
}

export function useProjectAutomations(projectId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.projectAutomations(projectId),
    queryFn: () => api.listProjectAutomations(projectId),
    enabled: enabled && !!projectId,
  });
}

export function useProjectAutomation(projectId: string, automationId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.projectAutomation(projectId, automationId),
    queryFn: () => api.getProjectAutomation(projectId, automationId),
    enabled: enabled && !!projectId && !!automationId,
  });
}

export function useAutomationExecutions(automationId: string, state = '', enabled = true) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: qk.automationExecutions(automationId, state),
    queryFn: ({ pageParam }) => api.listAutomationExecutions(
      automationId, pageParam ?? undefined, state || undefined,
    ),
    initialPageParam: null as string | null,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
    enabled: enabled && !!automationId,
    refetchInterval: (query) => automationExecutionPollInterval(
      query.state.data?.pages.flatMap((page) => page.items),
    ),
  });
}

export function useAutomationUsage(automationId: string, from = '', to = '', enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.automationUsage(automationId, from, to),
    queryFn: () => api.getAutomationUsage(automationId, from || undefined, to || undefined),
    enabled: enabled && !!automationId,
  });
}

export function useProjectUsage(
  projectId: string,
  groupBy: 'service' | 'automation' | 'model',
  from: string,
  to: string,
  enabled = true,
) {
  const api = useApi();
  return useQuery({
    queryKey: qk.projectUsage(projectId, groupBy, from, to),
    queryFn: () => api.getProjectUsage(projectId, groupBy, from, to),
    enabled: enabled && !!projectId,
  });
}

export function useServiceUsage(serviceId: string, from: string, to: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.serviceUsage(serviceId, from, to),
    queryFn: () => api.getServiceUsage(serviceId, from || undefined, to || undefined),
    enabled: enabled && !!serviceId,
  });
}

export function useAccountUsage(
  groupBy: 'device' | 'model' | 'grant',
  from: string,
  to: string,
  enabled = true,
) {
  const api = useApi();
  return useQuery({
    queryKey: qk.accountUsage(groupBy, from, to),
    queryFn: () => api.getAccountUsage(groupBy, from, to),
    enabled,
  });
}

export function useRunAutomationNow(automationId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (idempotencyKey: string) => api.runAutomationNow(automationId, idempotencyKey),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['automation-executions', automationId] }),
  });
}

export function useProviderCapabilities(provider: ProviderKind, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.providerCapabilities(provider),
    queryFn: () => api.getProviderCapabilities(provider),
    enabled,
    staleTime: 5 * 60_000,
    retry: false,
  });
}

export function useModelPricingRevisions(modelId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.modelPricingRevisions(modelId),
    queryFn: () => api.listModelPricingRevisions(modelId),
    enabled: enabled && !!modelId,
  });
}

export function useCreateModelPricingRevision(modelId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateModelPricingRevisionInput) =>
      api.createModelPricingRevision(modelId, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.modelPricingRevisions(modelId) }),
  });
}

export function useCreateProjectAutomation(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateProjectAutomationInput) => api.createProjectAutomation(projectId, input),
    onSuccess: (automation) => {
      qc.invalidateQueries({ queryKey: qk.projectAutomations(projectId) });
      qc.setQueryData(qk.projectAutomation(projectId, automation.automation.id), automation);
    },
  });
}

export function useUpdateProjectAutomation(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ automationId, input }: { automationId: string; input: UpdateProjectAutomationInput }) =>
      api.updateProjectAutomation(projectId, automationId, input),
    onSuccess: (automation) => {
      qc.invalidateQueries({ queryKey: qk.projectAutomations(projectId) });
      qc.setQueryData(qk.projectAutomation(projectId, automation.automation.id), automation);
    },
  });
}

export function useDeleteProjectAutomation(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (automationId: string) => api.deleteProjectAutomation(projectId, automationId),
    onSuccess: (_void, automationId) => {
      qc.invalidateQueries({ queryKey: qk.projectAutomations(projectId) });
      qc.removeQueries({ queryKey: qk.projectAutomation(projectId, automationId) });
    },
  });
}

export function useClusterProviderConfig(provider: ProviderKind, enabled = true) {
  const api = useApi();
  return useQuery({ queryKey: qk.clusterProvider(provider), queryFn: () => api.getClusterProviderConfig(provider), enabled });
}
export function useUpdateClusterProviderConfig(provider: ProviderKind) {
  const api = useApi(); const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: UpdateClusterProviderConfigInput) => api.updateClusterProviderConfig(provider, input),
    onSuccess: (saved) => qc.setQueryData(qk.clusterProvider(provider), saved),
  });
}
export function useTestClusterProviderConfig(provider: ProviderKind) {
  const api = useApi(); const qc = useQueryClient();
  return useMutation({ mutationFn: () => api.testClusterProviderConfig(provider), onSuccess: () => qc.invalidateQueries({ queryKey: qk.clusterProvider(provider) }) });
}

/* ---- project-scoped API keys (F12 / D24) --------------------------------- */

/** A project's API keys (owner only — the server 403s anyone else). */
export function useApiKeys(projectId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.apiKeys(projectId),
    queryFn: () => api.listApiKeys(projectId),
    enabled: enabled && !!projectId,
  });
}

export function useCreateApiKey(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateApiKeyInput): Promise<CreateApiKeyResponse> =>
      api.createApiKey(projectId, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.apiKeys(projectId) }),
  });
}

export function useRevokeApiKey(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (keyId: string) => api.revokeApiKey(projectId, keyId),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.apiKeys(projectId) }),
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
 * User search for account/member pickers. By default an empty query stays quiet;
 * Account access management opts in so the initial Account page is useful.
 */
export function useSearchUsers(q: string, enabled = q.trim().length > 0) {
  const api = useApi();
  return useQuery({
    queryKey: qk.users(q),
    queryFn: () => api.searchUsers(q),
    enabled,
  });
}
