/*
 * queries.ts — TanStack Query hooks over the ApiClient. Query keys are
 * centralised so SSE/status changes can invalidate precisely.
 */
import { useEffect } from 'react';
import {
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { useApi } from './ApiProvider';
import type {
  AddMemberInput,
  CreateAutomationInput,
  CreateApiKeyInput,
  CreateApiKeyResponse,
  CreateIntegrationInput,
  CreateKanbanLinkInput,
  CreateModelInput,
  CreateModelProviderInput,
  CreateProviderModelInput,
  CreateProjectInput,
  CreateRunInput,
  CreateScheduleInput,
  CreateServiceInput,
  Integration,
  KanbanConnectStatus,
  Member,
  Model,
  Project,
  ResumeSessionOptions,
  Run,
  ServiceWebhookSetup,
  UpdateIntegrationInput,
  UpdateAutomationInput,
  UpdateKanbanConfigInput,
  UpdateModelInput,
  UpdateModelProviderInput,
  UpdateProjectInput,
  UpdateScheduleInput,
  UpdateServiceInput,
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
  models: ['models'] as const,
  modelProviders: ['model-providers'] as const,
  modelProviderCatalog: (id: string) => ['model-provider-catalog', id] as const,
  projectModels: (projectId: string) => ['project-models', projectId] as const,
  kanbanLinks: ['kanban-links'] as const,
  kanbanConfig: ['kanban-config'] as const,
  projectKanbanLinks: (projectId: string) => ['project-kanban-links', projectId] as const,
  // D31: the member+ reduced board-link list that gates the "Kanban" header
  // button + feeds the embed modal's selector (distinct from the owner-only
  // projectKanbanLinks above).
  projectBoardLinks: (projectId: string) => ['project-board-links', projectId] as const,
  // D29: kanban discovery pickers — the caller's jtype workspaces, and a
  // workspace's boards (with columns), scoped to the project.
  jtypeWorkspaces: (projectId: string) => ['jtype-workspaces', projectId] as const,
  jtypeBoards: (projectId: string, workspaceId: string) =>
    ['jtype-boards', projectId, workspaceId] as const,
  // D28: an in-flight "Connect with jtype" device flow, keyed by its opaque
  // connect_id (cluster) or by (project, link, connect) for a per-link flow.
  kanbanConnect: (connectId: string) => ['kanban-connect', connectId] as const,
  linkConnect: (projectId: string, linkId: string, connectId: string) =>
    ['link-connect', projectId, linkId, connectId] as const,
  serviceSchedules: (serviceId: string) => ['service-schedules', serviceId] as const,
  serviceAutomations: (serviceId: string) => ['service-automations', serviceId] as const,
  integrations: (projectId: string) => ['integrations', projectId] as const,
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

/* ---- model catalog + project grants (D21) -------------------------------- */

/** The whole model catalog (cluster-admin). Powers the Cluster page ModelCard. */
export function useModels(enabled = true) {
  const api = useApi();
  return useQuery({ queryKey: qk.models, queryFn: () => api.listModels(), enabled });
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
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.models }),
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

/** Explicit provider webhook synchronization for one service's Automation page. */
export function useEnsureServiceWebhook() {
  const api = useApi();
  return useMutation({
    mutationFn: (serviceId: string): Promise<ServiceWebhookSetup> =>
      api.ensureServiceWebhook(serviceId),
  });
}

/* ---- kanban links (Feature E / F6) --------------------------------------- */

/** Cluster-admin READ-ONLY overview of every kanban link across all projects. */
export function useKanbanLinks(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.kanbanLinks,
    queryFn: () => api.listKanbanLinks(),
    enabled,
  });
}

/** A project's kanban links (owner-managed, F6 / D25). */
export function useProjectKanbanLinks(projectId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.projectKanbanLinks(projectId),
    queryFn: () => api.listProjectKanbanLinks(projectId),
    enabled: enabled && !!projectId,
  });
}

/**
 * D31: the member+ board-embed link list — gates the project header's "Kanban"
 * button and populates the modal's link selector. Distinct from
 * {@link useProjectKanbanLinks} (owner-only, leaks credential posture): this
 * endpoint is member+ and returns no credential fields, so a viewer / non-member
 * gets a 403 → empty data → no button.
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

export function useCreateProjectKanbanLink(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateKanbanLinkInput) => api.createProjectKanbanLink(projectId, input),
    onSuccess: () => {
      // Owner management and the member+ embed list are deliberately separate
      // endpoints. Refresh both so the Project header never keeps a stale
      // Kanban button after a link is added.
      qc.invalidateQueries({ queryKey: qk.projectKanbanLinks(projectId) });
      qc.invalidateQueries({ queryKey: qk.projectBoardLinks(projectId) });
      qc.invalidateQueries({ queryKey: qk.kanbanLinks });
    },
  });
}

export function useUpdateProjectKanbanLinkToken(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ linkId, token }: { linkId: string; token: string }) =>
      api.updateProjectKanbanLinkToken(projectId, linkId, token),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.projectKanbanLinks(projectId) });
      qc.invalidateQueries({ queryKey: qk.projectBoardLinks(projectId) });
      qc.invalidateQueries({ queryKey: qk.kanbanLinks });
    },
  });
}

export function useDeleteProjectKanbanLink(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (linkId: string) => api.deleteProjectKanbanLink(projectId, linkId),
    onSuccess: () => {
      // Deleting the final enabled link must hide the member+ embed affordance
      // immediately rather than leaving a button that opens an empty modal.
      qc.invalidateQueries({ queryKey: qk.projectKanbanLinks(projectId) });
      qc.invalidateQueries({ queryKey: qk.projectBoardLinks(projectId) });
      qc.invalidateQueries({ queryKey: qk.kanbanLinks });
    },
  });
}

/* ---- kanban discovery pickers (D29) -------------------------------------- */

/**
 * The caller's jtype workspaces for the create-link workspace picker. `retry:
 * false` so a typed 409/503/400 (integration off / unreachable / bad token)
 * surfaces at once as isError — the form auto-falls-back to manual entry and
 * shows the server message (fail-visible), never a spinner that never resolves.
 */
export function useJtypeWorkspaces(projectId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: qk.jtypeWorkspaces(projectId),
    queryFn: () => api.listJtypeWorkspaces(projectId),
    enabled: enabled && !!projectId,
    retry: false,
    staleTime: 30_000,
  });
}

/**
 * A workspace's boards (with columns) for the board + column pickers. Only fires
 * once a workspace is chosen; same fail-visible retry:false as the workspaces
 * query. Each board carries its `columns`, so the column selects read from this
 * cache without a further request.
 */
export function useJtypeBoards(projectId: string, workspaceId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: qk.jtypeBoards(projectId, workspaceId),
    queryFn: () => api.listJtypeBoards(projectId, workspaceId),
    enabled: enabled && !!projectId && !!workspaceId,
    retry: false,
    staleTime: 30_000,
  });
}

/* ---- cluster kanban config (D27) ----------------------------------------- */

/**
 * The cluster jtype config (cluster-admin). Powers the Cluster page KanbanCard's
 * editable form + source badge. `enabled` gates the fetch (the whole Cluster page
 * is cluster-admin only, so the default suffices there).
 */
export function useKanbanConfig(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.kanbanConfig,
    queryFn: () => api.getKanbanConfig(),
    enabled,
  });
}

/**
 * Set the cluster jtype config (base_url + optional token). Runtime-effective —
 * the resolver-backed poller/writeback pick it up without a restart (D27) — so we
 * invalidate the /system snapshot AND the link overview alongside the config:
 * flipping the integration on/off changes every link's effective credential state.
 */
export function useUpdateKanbanConfig() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: UpdateKanbanConfigInput) => api.updateKanbanConfig(input),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.system });
      qc.invalidateQueries({ queryKey: qk.kanbanConfig });
      qc.invalidateQueries({ queryKey: qk.kanbanLinks });
      // Every project's link list is now stale too: per-link credential_status
      // depends on the effective cluster token (prefix match — all project ids).
      qc.invalidateQueries({ queryKey: ['project-kanban-links'] });
    },
  });
}

/** Clear the cluster jtype DB override, falling back to env/off (D27). */
export function useDeleteKanbanConfig() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.deleteKanbanConfig(),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.system });
      qc.invalidateQueries({ queryKey: qk.kanbanConfig });
      qc.invalidateQueries({ queryKey: qk.kanbanLinks });
      // See useUpdateKanbanConfig: credential_status is cluster-config-derived.
      qc.invalidateQueries({ queryKey: ['project-kanban-links'] });
    },
  });
}

/* ---- kanban "Connect with jtype" device flow (D28) ----------------------- */

/**
 * The two credential views a completed device flow makes stale: the cluster
 * config (token_set + token_expires_at flip) and every kanban-link list (a
 * per-link connect flips credential_status → per_link). Both connect hooks
 * invalidate this set once, on the pending→complete edge.
 */
function invalidateKanbanCredentials(qc: ReturnType<typeof useQueryClient>): void {
  qc.invalidateQueries({ queryKey: qk.kanbanConfig });
  qc.invalidateQueries({ queryKey: qk.kanbanLinks });
  // Prefix match — every project's link list (reuses D27's set).
  qc.invalidateQueries({ queryKey: ['project-kanban-links'] });
}

/** Should a device-flow poll keep going? Only while the last result was pending. */
function connectRefetchInterval(status: 'error' | 'pending' | 'success', data: unknown): number | false {
  // A 404 connect_expired (or any poll error) is terminal — stop, the UI treats
  // it as "expired, reconnect". `retry:false` keeps that a single request.
  if (status === 'error') return false;
  return (data as KanbanConnectStatus | undefined)?.status === 'pending' ? 2500 : false;
}

/**
 * Poll a CLUSTER "Connect with jtype" flow while it is pending (every 2.5s),
 * stopping on any terminal state (complete/expired/denied/unsupported) or a 404
 * connect_expired. On the complete edge, refresh the credential views so the
 * token_set badge + expiry flip without a manual reload.
 */
export function useKanbanConnectStatus(connectId: string | undefined, enabled: boolean) {
  const api = useApi();
  const qc = useQueryClient();
  const query = useQuery({
    queryKey: qk.kanbanConnect(connectId ?? ''),
    queryFn: () => api.pollKanbanConnect(connectId!),
    enabled: enabled && !!connectId,
    retry: false,
    refetchInterval: (q) => connectRefetchInterval(q.state.status, q.state.data),
  });
  const complete = query.data?.status === 'complete';
  useEffect(() => {
    if (complete) invalidateKanbanCredentials(qc);
  }, [complete, qc]);
  return query;
}

/** Per-link equivalent of {@link useKanbanConnectStatus}. */
export function useLinkConnectStatus(
  projectId: string,
  linkId: string,
  connectId: string | undefined,
  enabled: boolean,
) {
  const api = useApi();
  const qc = useQueryClient();
  const query = useQuery({
    queryKey: qk.linkConnect(projectId, linkId, connectId ?? ''),
    queryFn: () => api.pollLinkConnect(projectId, linkId, connectId!),
    enabled: enabled && !!connectId,
    retry: false,
    refetchInterval: (q) => connectRefetchInterval(q.state.status, q.state.data),
  });
  const complete = query.data?.status === 'complete';
  useEffect(() => {
    if (complete) invalidateKanbanCredentials(qc);
  }, [complete, qc]);
  return query;
}

/**
 * Start a cluster device flow. On success we seed the poll cache with a pending
 * status keyed by the new connect_id, so the flow panel shows the user_code +
 * live status the instant the caller flips on {@link useKanbanConnectStatus}.
 */
export function useStartKanbanConnect() {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.startKanbanConnect(),
    onSuccess: (start) => {
      qc.setQueryData<KanbanConnectStatus>(qk.kanbanConnect(start.connect_id), {
        status: 'pending',
        token_set: false,
      });
    },
  });
}

/** Start a per-link device flow (seeds the per-link poll cache; see above). */
export function useStartLinkConnect(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (linkId: string) => api.startLinkConnect(projectId, linkId),
    onSuccess: (start, linkId) => {
      qc.setQueryData<KanbanConnectStatus>(qk.linkConnect(projectId, linkId, start.connect_id), {
        status: 'pending',
        token_set: false,
      });
    },
  });
}

/* ---- schedules (F11 / D24) ----------------------------------------------- */

/** A service's cron triggers (member+ read). */
export function useServiceSchedules(serviceId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.serviceSchedules(serviceId),
    queryFn: () => api.listServiceSchedules(serviceId),
    enabled: enabled && !!serviceId,
  });
}

export function useCreateServiceSchedule(serviceId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateScheduleInput) => api.createServiceSchedule(serviceId, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.serviceSchedules(serviceId) }),
  });
}

export function useUpdateSchedule(serviceId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ scheduleId, input }: { scheduleId: string; input: UpdateScheduleInput }) =>
      api.updateSchedule(scheduleId, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.serviceSchedules(serviceId) }),
  });
}

export function useDeleteSchedule(serviceId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (scheduleId: string) => api.deleteSchedule(scheduleId),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.serviceSchedules(serviceId) }),
  });
}

/* ---- provider-event Automations ----------------------------------------- */

export function useServiceAutomations(serviceId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.serviceAutomations(serviceId),
    queryFn: () => api.listServiceAutomations(serviceId),
    enabled: enabled && !!serviceId,
  });
}

export function useCreateServiceAutomation(serviceId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateAutomationInput) => api.createServiceAutomation(serviceId, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.serviceAutomations(serviceId) }),
  });
}

export function useUpdateAutomation(serviceId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ automationId, input }: { automationId: string; input: UpdateAutomationInput }) =>
      api.updateAutomation(automationId, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.serviceAutomations(serviceId) }),
  });
}

export function useDeleteAutomation(serviceId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (automationId: string) => api.deleteAutomation(automationId),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.serviceAutomations(serviceId) }),
  });
}

/* ---- integrations (D19 / F5) --------------------------------------------- */

/** A project's git integrations (member+ read). */
export function useIntegrations(projectId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: qk.integrations(projectId),
    queryFn: () => api.listIntegrations(projectId),
    enabled: enabled && !!projectId,
  });
}

export function useCreateIntegration(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateIntegrationInput) => api.createIntegration(projectId, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.integrations(projectId) }),
  });
}

export function useUpdateIntegration(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ integrationId, input }: { integrationId: string; input: UpdateIntegrationInput }): Promise<Integration> =>
      api.updateIntegration(integrationId, input),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.integrations(projectId) }),
  });
}

export function useDeleteIntegration(projectId: string) {
  const api = useApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (integrationId: string) => api.deleteIntegration(integrationId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.integrations(projectId) });
      qc.invalidateQueries({ queryKey: qk.project(projectId) });
      qc.invalidateQueries({ queryKey: qk.services(projectId) });
    },
  });
}

/** Repos the integration's bot token can see (for the service repo picker). */
export function useIntegrationRepos(projectId: string, integrationId: string, q: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: ['integration-repos', projectId, integrationId, q],
    queryFn: () => api.listIntegrationRepos(projectId, integrationId, q),
    enabled: enabled && !!projectId && !!integrationId,
    staleTime: 30_000,
    retry: false,
  });
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
