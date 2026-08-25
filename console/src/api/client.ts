/*
 * client.ts — the ApiClient interface + the real HTTP implementation.
 *
 * Both the HTTP client and the in-memory mock (mockClient.ts) implement
 * ApiClient, so the whole app is demo-able / e2e-testable without a cluster by
 * flipping VITE_DEMO=1. This is the ONE module that knows the wire format; if
 * 11-api.md drifts from the fallback route spec, reconcile it here.
 */
import type {
  JTypeCloudDocument,
  JTypeDocumentListItem,
  JTypeSaveDocumentRequest,
  JTypeSaveDocumentResponse,
} from 'jtype-board-react';
import type {
  AddMemberInput,
  AccountRepositoryCatalog,
  AccountTaskResponse,
  ApiKey,
  ApiKeysEnvelope,
  AuthProviderInfo,
  AuthProvidersEnvelope,
  BoardEmbedLink,
  CatalogModel,
  ClusterProviderConfig,
  CreateApiKeyInput,
  CreateApiKeyResponse,
  CreateKanbanCardOccurrenceInput,
  CreateKanbanCardOccurrenceResponse,
  CreateProjectAutomationInput,
  CreateProjectInput,
  CreateModelInput,
  CreateModelProviderInput,
  CreateProviderModelInput,
  CreateRunInput,
  CreateServiceInput,
  EventsEnvelope,
  Me,
  Member,
  MembersEnvelope,
  Model,
  ModelProvider,
  ModelProviderVerification,
  ProviderModel,
  PrInfo,
  Project,
  ProjectAutomationSpec,
  AutomationExecution,
  AutomationExecutionsPage,
  ProjectPlugin,
  PluginAuditEvent,
  PluginConsentInput,
  PluginInstallStart,
  GitHubAppInstallation,
  GitHubInstallationConsentPreview,
  JTypePluginConnectStatus,
  KanbanCardExecutionsPage,
  ProviderKind,
  PluginRepositoryResource,
  PluginWorkspaceResource,
  PluginBoardResource,
  ScmProviderCapabilities,
  SetupStatus,
  SetupInput,
  StartAccountTaskInput,
  ProjectModels,
  ProjectModel,
  ProjectsEnvelope,
  ProviderRepo,
  Run,
  RunAttachmentIntent,
  RunArtifact,
  RunEvent,
  RunMessage,
  RunPermission,
  RunnerPrewarm,
  RetryRunOptions,
  ResumeSessionOptions,
  RunsEnvelope,
  Service,
  ServiceBranch,
  ServicesEnvelope,
  StreamFrame,
  SystemInfo,
  UpdateClusterProviderConfigInput,
  UpdateProjectAutomationInput,
  PutServiceKanbanInput,
  ServiceKanbanBinding,
  ServiceKanbanPolicy,
  UpdateModelInput,
  UpdateModelProviderInput,
  UpdateProjectInput,
  UpdateProviderModelInput,
  UpdateServiceInput,
  UserSearchResult,
  UsersEnvelope,
  UsageSummary,
  UsageSummaryEnvelope,
  ModelPricingRevision,
  CreateModelPricingRevisionInput,
} from './types';

// ApiError / apiErrorCode / TokenSource live in @jcloud/device-ui (M6 shared
// package) so the console and the device-relay layer throw/branch on ONE error
// class; re-exported here to keep every existing api/client import working.
import { ApiError, apiErrorCode } from '@jcloud/device-ui';
import type { TokenSource } from '@jcloud/device-ui';

export { ApiError, apiErrorCode };
export type { TokenSource };

/** Handle returned by streamRun; call close() to stop following. */
export interface StreamHandle {
  close: () => void;
}

export interface StreamCallbacks {
  onFrame: (frame: StreamFrame) => void;
  onError?: (err: unknown) => void;
  onOpen?: () => void;
}

export interface ApiClient {
  getSetupStatus(): Promise<SetupStatus>;
  updateSetup(input: SetupInput): Promise<SetupStatus>;
  getClusterProviderConfig(provider: ProviderKind): Promise<ClusterProviderConfig>;
  updateClusterProviderConfig(provider: ProviderKind, input: UpdateClusterProviderConfigInput): Promise<ClusterProviderConfig>;
  testClusterProviderConfig(provider: ProviderKind): Promise<ClusterProviderConfig>;
  getClusterProviderImpact(provider: ProviderKind): Promise<{ affected_installations: number; affected_projects: number }>;
  /**
   * GET /api/v1/me — the current principal (user / identities / is_service).
   * 200 for every authenticated principal; only an unauthenticated request 401s.
   */
  getMe(): Promise<Me>;

  listProjects(): Promise<Project[]>;
  /** Repositories visible to the current Account; no Cloud association required. */
  listAccountRepositories(q?: string): Promise<AccountRepositoryCatalog>;
  /** Branches for an Account-visible Repository before it is materialized. */
  listAccountRepositoryBranches(provider: string, providerRepoId: string): Promise<ServiceBranch[]>;
  /** Resolve an Account repository and create its task in one request. */
  startAccountTask(input: StartAccountTaskInput): Promise<AccountTaskResponse>;
  createProject(input: CreateProjectInput): Promise<Project>;
  getProject(id: string): Promise<Project>;
  /** PATCH /projects/{id} — only the provided fields are updated (11-api.md §2.1). */
  updateProject(id: string, input: UpdateProjectInput): Promise<Project>;
  /** DELETE /projects/{id} — cascades runs/events/artifacts; 204 No Content. */
  deleteProject(id: string): Promise<void>;

  listRuns(projectId: string): Promise<Run[]>;
  getRun(runId: string): Promise<Run>;
  cancelRun(runId: string): Promise<Run>;
  retryRun(runId: string, options?: RetryRunOptions): Promise<Run>;
  /**
   * POST /api/v1/runs/{id}/resume — continue a FINISHED session run in a new run
   * that reloads the same ACP session (F9b / D23 ①②). 409 run_not_resumable
   * (not a session / still active), session_not_recorded (no ACP session id), or
   * workspace_not_persistent (no PVC to reload the transcript from).
   */
  resumeSession(runId: string, prompt: string, options?: ResumeSessionOptions): Promise<Run>;

  /* ---- multi-turn session (D22) ------------------------------------------ */
  /**
   * POST /api/v1/runs/{id}/messages — feed a follow-up prompt to a session run
   * (status must be awaiting_input or running; otherwise 409 run_not_awaiting).
   */
  sendMessage(runId: string, prompt: string): Promise<RunMessage>;
  /**
   * POST /api/v1/runs/{id}/finish — wind the session down: the runner exits
   * gracefully and the run converges to succeeded. Idempotent.
   */
  finishSession(runId: string): Promise<Run>;
  /**
   * POST /api/v1/runs/{id}/permission-response — answer a pending permission
   * request of an approval-mode session (F8b). 409 permission_already_resolved
   * when someone (or the runner's timeout) beat us to it; 400 invalid_option
   * for an option the request never offered; 403 for viewers.
   */
  respondPermission(runId: string, requestId: string, optionId: string): Promise<RunPermission>;

  /* ---- PR review (blueprint §4/§5) --------------------------------------- */
  /** GET /api/v1/runs/{id}/pr — the run's PR, live state, and its review runs. */
  getPR(runId: string): Promise<PrInfo>;
  /**
   * POST /api/v1/runs/{id}/review — request an AI review of a succeeded agent
   * run's PR. Returns the newly-created kind=review run.
   */
  requestReview(runId: string): Promise<Run>;

  /** Replay events with seq > afterSeq (0 = from start), optionally bounded. */
  listEvents(runId: string, afterSeq?: number, limit?: number): Promise<RunEvent[]>;
  /** Subscribe to the live SSE stream (replay-after-seq then live). */
  streamRun(
    runId: string,
    afterSeq: number,
    cb: StreamCallbacks,
  ): StreamHandle;

  getDiff(runId: string): Promise<RunArtifact>;
  /** Absolute-ish URL for downloading the raw .diff (used by an <a download>). */
  diffDownloadUrl(runId: string): string;

  /**
   * GET /api/v1/system — the read-only cluster-admin snapshot (capacity,
   * guardrails, provider, runner, version, auth). Never carries a secret.
   */
  getSystem(): Promise<SystemInfo>;

  /**
   * POST /api/v1/system/runner-image/prewarm — the Cluster page "sync runner
   * image" action (cluster-admin): (re)assert the prewarm DaemonSet so every
   * node re-pulls the current runner image. 409 prewarm_not_supported when the
   * launcher has no cluster. Returns the post-sync prewarm snapshot.
   */
  prewarmRunnerImage(): Promise<RunnerPrewarm>;

  /* ---- model providers + catalog discovery ----------------------------- */
  listModelProviders(): Promise<ModelProvider[]>;
  createModelProvider(input: CreateModelProviderInput): Promise<ModelProvider>;
  updateModelProvider(id: string, input: UpdateModelProviderInput): Promise<ModelProvider>;
  deleteModelProvider(id: string): Promise<void>;
  verifyModelProvider(id: string): Promise<ModelProviderVerification>;
  getModelProviderCatalog(id: string): Promise<CatalogModel[]>;
  createProviderModel(id: string, input: CreateProviderModelInput): Promise<ProviderModel>;

  /* ---- project-owned model providers (M2; owner write / member read) ---- */
  /** GET /api/v1/projects/{id}/model-providers — the project's own providers (member+). Unwraps `{providers}`. */
  listProjectModelProviders(projectId: string): Promise<ModelProvider[]>;
  /** POST /api/v1/projects/{id}/model-providers — add a project provider (owner). */
  createProjectModelProvider(projectId: string, input: CreateModelProviderInput): Promise<ModelProvider>;
  /** PATCH /api/v1/projects/{id}/model-providers/{pid} — edit a project provider (owner). */
  updateProjectModelProvider(projectId: string, id: string, input: UpdateModelProviderInput): Promise<ModelProvider>;
  /** DELETE /api/v1/projects/{id}/model-providers/{pid} — remove a project provider (owner). */
  deleteProjectModelProvider(projectId: string, id: string): Promise<void>;
  /** POST /api/v1/projects/{id}/model-providers/{pid}/verify — probe reachability (owner). */
  verifyProjectModelProvider(projectId: string, id: string): Promise<ModelProviderVerification>;
  /** GET /api/v1/projects/{id}/model-providers/{pid}/catalog — discovered models (owner). Unwraps `{models}`; 409 catalog_unavailable when disabled. */
  getProjectModelProviderCatalog(projectId: string, id: string): Promise<CatalogModel[]>;
  /** POST /api/v1/projects/{id}/model-providers/{pid}/models — add a model (owner). */
  createProjectProviderModel(projectId: string, id: string, input: CreateProviderModelInput): Promise<ProviderModel>;
  /** PATCH /api/v1/projects/{id}/model-providers/{pid}/models/{mid} — edit a model incl. its enabled toggle (owner). */
  updateProjectProviderModel(projectId: string, providerId: string, modelId: string, input: UpdateProviderModelInput): Promise<ProviderModel>;
  /** DELETE /api/v1/projects/{id}/model-providers/{pid}/models/{mid} — remove a model (owner). */
  deleteProjectProviderModel(projectId: string, providerId: string, modelId: string): Promise<void>;

  /* ---- model catalog + account/project grants (D21) --------------------- */
  /** GET /api/v1/system/models — the whole catalog (cluster-admin). */
  listModels(): Promise<Model[]>;
  /** GET /api/v1/account/models — models directly authorized for this Account. */
  listAccountModels(): Promise<ProjectModel[]>;
  /** POST /api/v1/system/models — add a model (cluster-admin). */
  createModel(input: CreateModelInput): Promise<Model>;
  /** PATCH /api/v1/system/models/{id} — edit a model (cluster-admin). */
  updateModel(id: string, input: UpdateModelInput): Promise<Model>;
  /** DELETE /api/v1/system/models/{id} — remove a model (cluster-admin). */
  deleteModel(id: string): Promise<void>;
  /** PUT /api/v1/system/models/{id}/grants/{projectId} — authorize a project. */
  grantModel(modelId: string, projectId: string): Promise<Model>;
  /** DELETE /api/v1/system/models/{id}/grants/{projectId} — revoke. */
  revokeModel(modelId: string, projectId: string): Promise<Model>;
  /** PUT /api/v1/system/models/{id}/account-grants/{userId} — authorize every Desktop for an Account. */
  grantModelToAccount(modelId: string, userId: string): Promise<Model>;
  /** DELETE /api/v1/system/models/{id}/account-grants/{userId} — revoke Account-wide Desktop access. */
  revokeModelFromAccount(modelId: string, userId: string): Promise<Model>;
  /**
   * GET /api/v1/projects/{id}/models — models granted to a project (member+).
   * Carries only id/name/model_name plus env_fallback; never a base_url or key.
   */
  listProjectModels(projectId: string): Promise<ProjectModels>;

  /* ---- kanban board embed (D31) ----------------------------------------- */
  /**
   * GET /api/v1/projects/{id}/kanban/board/links — the reduced, member+ list of
   * the project's kanban links (no credential fields). Gates the "Kanban" header
   * button and feeds the board-embed modal's link selector. 403 for a viewer /
   * non-member (→ empty list → no button). Links are derived from current
   * Service Kanban Automations, never from a separately managed credential row.
   */
  listProjectBoardLinks(projectId: string): Promise<BoardEmbedLink[]>;
  /**
   * GET /api/v1/projects/{id}/kanban/board/documents?workspace=<ws> — proxies
   * jtype's `listDocuments` for a workspace linked to this project (member+). The
   * effective jtype token is applied server-side and never crosses the wire;
   * the response is jtype's native `JTypeDocumentListItem[]` passed through
   * verbatim.
   */
  boardListDocuments(projectId: string, workspaceId: string): Promise<JTypeDocumentListItem[]>;
  /**
   * GET /api/v1/projects/{id}/kanban/board/documents/{docId}?workspace=<ws> —
   * proxies jtype's `getDocument` (member+; verbatim `JTypeCloudDocument`).
   */
  boardGetDocument(
    projectId: string,
    workspaceId: string,
    docId: string,
  ): Promise<JTypeCloudDocument>;
  /**
   * POST /api/v1/projects/{id}/kanban/board/documents/save?workspace=<ws> —
   * proxies jtype's `saveDocument` for card create/edit/move (member+; matches
   * run-dispatch authority). Returns jtype's native `JTypeSaveDocumentResponse`.
   */
  boardSaveDocument(
    projectId: string,
    workspaceId: string,
    req: JTypeSaveDocumentRequest,
  ): Promise<JTypeSaveDocumentResponse>;

  /* ---- project-scoped API keys (F12 / D24) ------------------------------- */
  /** GET /api/v1/projects/{id}/apikeys — the project's keys, owner only. */
  listApiKeys(projectId: string): Promise<ApiKey[]>;
  /**
   * POST /api/v1/projects/{id}/apikeys — mint a key (owner only). The response
   * carries the plaintext `key` exactly once — there is no read-back endpoint.
   */
  createApiKey(projectId: string, input: CreateApiKeyInput): Promise<CreateApiKeyResponse>;
  /** DELETE /api/v1/projects/{id}/apikeys/{keyId} — revoke, effective immediately (owner only). */
  revokeApiKey(projectId: string, keyId: string): Promise<void>;

  /* ---- services (blueprint §4) ------------------------------------------- */
  /** GET /api/v1/repositories — every Repository visible to the current Account. */
  listRepositories(): Promise<Service[]>;
  /** GET /api/v1/repositories/{id} — one Repository detail. */
  getRepository(repositoryId: string): Promise<Service>;
  /** GET /api/v1/projects/{id}/repositories — the project's repo configurations. */
  listServices(projectId: string): Promise<Service[]>;
  /** POST /api/v1/projects/{id}/repositories — add a repository to a project. */
  createService(projectId: string, input: CreateServiceInput): Promise<Service>;
  /** PATCH /api/v1/repositories/{id} — edit a service (owner); default model (D21). */
  updateService(serviceId: string, input: UpdateServiceInput): Promise<Service>;
  /** DELETE /api/v1/repositories/{id} — stop runs and cascade-delete a service (owner). */
  deleteService(serviceId: string): Promise<void>;
  /* ---- unified project plugins (plugin-platform v1) --------------------- */
  /** GET /projects/{id}/plugins — fixed Provider cards, member+ read. */
  listProjectPlugins(projectId: string): Promise<ProjectPlugin[]>;
  /** Starts consent-first installation; owner/admin only. */
  startPluginInstall(projectId: string, provider: ProviderKind, input: PluginConsentInput): Promise<PluginInstallStart>;
  listGitHubAppInstallations(projectId: string): Promise<GitHubAppInstallation[]>;
  previewGitHubAppInstallationConsent(projectId: string, installationId: string): Promise<GitHubInstallationConsentPreview>;
  selectGitHubAppInstallation(projectId: string, installationId: string, input: PluginConsentInput): Promise<ProjectPlugin>;
  getJTypePluginConnectStatus(projectId: string, installationId: string, connectId: string): Promise<JTypePluginConnectStatus>;
  setProjectPluginEnabled(installationId: string, enabled: boolean): Promise<ProjectPlugin>;
  setProjectPluginWorkspace(installationId: string, workspaceId: string): Promise<ProjectPlugin>;
  /** DELETE is intentionally destructive; the Console requires typed confirmation after loading its impact. */
  getProjectPluginImpact(projectId: string, installationId: string): Promise<{ services: number; automations: number }>;
  listProjectPluginAudit(projectId: string, installationId: string): Promise<PluginAuditEvent[]>;
  uninstallProjectPlugin(installationId: string, force?: boolean): Promise<void>;
  listPluginRepositories(projectId: string, installationId: string, q?: string): Promise<PluginRepositoryResource[]>;
  listPluginWorkspaces(projectId: string, installationId: string): Promise<PluginWorkspaceResource[]>;
  listPluginBoards(projectId: string, installationId: string, workspaceId?: string): Promise<PluginBoardResource[]>;
  getProviderCapabilities(provider: ProviderKind): Promise<ScmProviderCapabilities>;
  /** Generic Automation list; the editor is project-owned but every entry binds a Service. */
  listProjectAutomations(projectId: string): Promise<ProjectAutomationSpec[]>;
  getProjectAutomation(projectId: string, automationId: string): Promise<ProjectAutomationSpec>;
  createProjectAutomation(projectId: string, input: CreateProjectAutomationInput): Promise<ProjectAutomationSpec>;
  updateProjectAutomation(projectId: string, automationId: string, input: UpdateProjectAutomationInput): Promise<ProjectAutomationSpec>;
  deleteProjectAutomation(projectId: string, automationId: string): Promise<void>;
  listAutomationExecutions(automationId: string, before?: string, state?: string, limit?: number): Promise<AutomationExecutionsPage>;
  getAutomationExecution(automationId: string, executionId: string): Promise<AutomationExecution>;
  runAutomationNow(automationId: string, idempotencyKey: string): Promise<AutomationExecution>;
  getAutomationUsage(automationId: string, from?: string, to?: string): Promise<UsageSummary>;
  getProjectUsage(projectId: string, groupBy: 'service' | 'automation' | 'model', from?: string, to?: string): Promise<UsageSummaryEnvelope>;
  getAccountUsage(groupBy: 'device' | 'model' | 'grant', from?: string, to?: string): Promise<UsageSummaryEnvelope>;
  getServiceUsage(serviceId: string, from?: string, to?: string): Promise<UsageSummary>;
  listModelPricingRevisions(modelId: string): Promise<ModelPricingRevision[]>;
  createModelPricingRevision(modelId: string, input: CreateModelPricingRevisionInput): Promise<ModelPricingRevision>;
  getServiceKanban(serviceId: string): Promise<ServiceKanbanBinding>;
  getServiceKanbanPolicy(serviceId: string): Promise<ServiceKanbanPolicy>;
  listServiceKanbanCardExecutions(serviceId: string, workspaceId: string, documentPath: string, before?: string, limit?: number): Promise<KanbanCardExecutionsPage>;
  createServiceKanbanOccurrence(serviceId: string, input: CreateKanbanCardOccurrenceInput): Promise<CreateKanbanCardOccurrenceResponse>;
  putServiceKanban(serviceId: string, input: PutServiceKanbanInput): Promise<ServiceKanbanBinding>;
  deleteServiceKanban(serviceId: string): Promise<void>;
  /** Lists branches through the Service's bound Plugin; never exposes a Git credential. */
  listServiceBranches(serviceId: string): Promise<ServiceBranch[]>;
  /** POST /api/v1/repositories/{id}/runs — dispatch a run against a specific service. */
  createServiceRun(serviceId: string, input: CreateRunInput): Promise<Run>;
  /** Stages one bounded Project attachment through Cloud; no object-store URL is exposed. */
  uploadRunAttachment(serviceId: string, file: File): Promise<RunAttachmentIntent>;
  /**
   * GET /providers/{id}/repos?q= — the Drone-style onboarding picker: repos the
   * caller's provider credential can see. 403 when no credential is linked.
   */
  listProviderRepos(provider: string, q?: string): Promise<ProviderRepo[]>;

  /* ---- members (blueprint §2) -------------------------------------------- */
  listMembers(projectId: string): Promise<Member[]>;
  addMember(projectId: string, input: AddMemberInput): Promise<Member>;
  removeMember(projectId: string, userId: string): Promise<void>;
  /** GET /api/v1/users?q= — user search for the add-member picker. */
  searchUsers(q: string): Promise<UserSearchResult[]>;
}

/* ------------------------------------------------------------------------- */

const BASE = '/api/v1';

function authHeaders(token: string | undefined): HeadersInit {
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function parseError(res: Response): Promise<never> {
  let body: unknown;
  let message = `${res.status} ${res.statusText}`;
  try {
    const text = await res.text();
    if (text) {
      try {
        body = JSON.parse(text);
        // 11-api.md §0: nested error envelope { error: { code, message } }.
        // Tolerate a few legacy shapes too.
        const asObj = body as {
          error?: string | { code?: string; message?: string };
          message?: string;
        };
        if (asObj.error && typeof asObj.error === 'object') {
          message = asObj.error.message || message;
        } else if (typeof asObj.error === 'string') {
          message = asObj.error;
        } else if (asObj.message) {
          message = asObj.message;
        }
      } catch {
        body = text;
        message = text.slice(0, 300);
      }
    }
  } catch {
    /* ignore */
  }
  throw new ApiError(res.status, message, body);
}

export interface HttpClientOptions {
  /**
   * Fired on any 401 — the session-level "token was revoked/rotated" signal.
   * The auth layer clears the stored token and routes back to the login gate.
   */
  onUnauthorized?: () => void;
}

function usageParams(from?: string, to?: string): string {
  const params = new URLSearchParams();
  if (from) params.set('from', from);
  if (to) params.set('to', to);
  const value = params.toString();
  return value ? `?${value}` : '';
}

export function createHttpClient(
  token: TokenSource,
  opts: HttpClientOptions = {},
): ApiClient {
  const getToken = typeof token === 'function' ? token : () => token;

  async function req<T>(
    path: string,
    init?: RequestInit,
  ): Promise<T> {
    const res = await fetch(`${BASE}${path}`, {
      ...init,
      // Primary auth is the httpOnly jcloud_session cookie (blueprint §2); a
      // same-origin fetch carries it automatically. The legacy console token, if
      // present, still rides as a Bearer header (Advanced path).
      credentials: 'same-origin',
      headers: {
        Accept: 'application/json',
        ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
        ...authHeaders(getToken()),
        ...init?.headers,
      },
    });
    if (res.status === 401) opts.onUnauthorized?.();
    if (!res.ok) return parseError(res);
    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }

  return {
    getSetupStatus: () => req<SetupStatus>('/setup'),
    updateSetup: (input) => req<SetupStatus>('/setup', { method: 'PUT', body: JSON.stringify(input) }),
    getClusterProviderConfig: (provider) => req<ClusterProviderConfig>(`/system/providers/${encodeURIComponent(provider)}`),
    updateClusterProviderConfig: (provider, input) => req<ClusterProviderConfig>(`/system/providers/${encodeURIComponent(provider)}`, { method: 'PUT', body: JSON.stringify(input) }),
    testClusterProviderConfig: (provider) => req<ClusterProviderConfig>(`/system/providers/${encodeURIComponent(provider)}/test`, { method: 'POST' }),
    getClusterProviderImpact: (provider) => req<{ affected_installations: number; affected_projects: number }>(`/system/providers/${encodeURIComponent(provider)}/impact`),
    getMe: () => req<Me>('/me'),

    // Lists are wrapped in envelopes (11-api.md §2); unwrap to bare arrays.
    listProjects: async () =>
      (await req<ProjectsEnvelope>('/projects')).projects,

    createProject: (input) =>
      req<Project>('/projects', {
        method: 'POST',
        body: JSON.stringify(input),
      }),

    getProject: (id) => req<Project>(`/projects/${encodeURIComponent(id)}`),

    updateProject: (id, input) =>
      req<Project>(`/projects/${encodeURIComponent(id)}`, {
        method: 'PATCH',
        body: JSON.stringify(input),
      }),

    deleteProject: async (id) => {
      await req<void>(`/projects/${encodeURIComponent(id)}`, {
        method: 'DELETE',
      });
    },

    // Project-scoped runs route (11-api.md §2.2).
    listRuns: async (projectId) =>
      (
        await req<RunsEnvelope>(
          `/projects/${encodeURIComponent(projectId)}/runs`,
        )
      ).runs,

    getRun: (runId) => req<Run>(`/runs/${encodeURIComponent(runId)}`),

    cancelRun: (runId) =>
      req<Run>(`/runs/${encodeURIComponent(runId)}/cancel`, {
        method: 'POST',
      }),

    retryRun: (runId, options) =>
      req<Run>(`/runs/${encodeURIComponent(runId)}/retry`, {
        method: 'POST',
		...(options?.model_id ? { body: JSON.stringify(options) } : {}),
      }),

    // Session resume (F9b).
    resumeSession: (runId, prompt, options) =>
      req<Run>(`/runs/${encodeURIComponent(runId)}/resume`, {
        method: 'POST',
        body: JSON.stringify({ prompt, ...options }),
      }),

    // Multi-turn session (D22).
    sendMessage: (runId, prompt) =>
      req<RunMessage>(`/runs/${encodeURIComponent(runId)}/messages`, {
        method: 'POST',
        body: JSON.stringify({ prompt }),
      }),

    finishSession: (runId) =>
      req<Run>(`/runs/${encodeURIComponent(runId)}/finish`, {
        method: 'POST',
      }),

    // Session permission approval (F8b).
    respondPermission: (runId, requestId, optionId) =>
      req<RunPermission>(`/runs/${encodeURIComponent(runId)}/permission-response`, {
        method: 'POST',
        body: JSON.stringify({ request_id: requestId, option_id: optionId }),
      }),

    getPR: (runId) => req<PrInfo>(`/runs/${encodeURIComponent(runId)}/pr`),

    requestReview: (runId) =>
      req<Run>(`/runs/${encodeURIComponent(runId)}/review`, {
        method: 'POST',
      }),

    listEvents: async (runId, afterSeq = 0, limit) => {
      const params = new URLSearchParams({ after_seq: String(afterSeq) });
      if (limit !== undefined) params.set('limit', String(limit));
      return (
        await req<EventsEnvelope>(
          `/runs/${encodeURIComponent(runId)}/events?${params}`,
        )
      ).events;
    },

    streamRun: (runId, afterSeq, cb) => {
      // Native EventSource cannot set Authorization headers, so the token rides
      // as a query param. The orchestrator accepts either for the stream route;
      // the proxy also forwards it. In prod the token is a same-origin secret.
      const params = new URLSearchParams({ after_seq: String(afterSeq) });
      const streamToken = getToken();
      if (streamToken) params.set('access_token', streamToken);
      const url = `${BASE}/runs/${encodeURIComponent(runId)}/stream?${params}`;
      const es = new EventSource(url);

      const handle = (e: MessageEvent) => {
        try {
          const data = JSON.parse(e.data) as StreamFrame['data'];
          cb.onFrame({ event: e.type, data });
        } catch (err) {
          cb.onError?.(err);
        }
      };

      es.onopen = () => cb.onOpen?.();
      // Default (unnamed) messages.
      es.onmessage = handle;
      // Named events per the contract.
      for (const t of [
        'run.status',
        'agent.text',
        'agent.tool_call',
        'agent.tool_result',
        'run.artifact',
        'run.failure',
        'run.git',
        'run.result',
        // F9b: the ACP session established/resumed system row.
        'run.session',
        // D22 session events: the user's follow-up bubbles and the wind-down row.
        'user.message',
        'session.finish',
        // F8b permission approval: the request card and its final outcome.
        'agent.permission_request',
        'agent.permission_resolved',
      ]) {
        es.addEventListener(t, handle as EventListener);
      }
      es.onerror = (err) => cb.onError?.(err);

      return { close: () => es.close() };
    },

    // Artifact route is singular with a `kind` query param (11-api.md §2.4).
    getDiff: (runId) =>
      req<RunArtifact>(
        `/runs/${encodeURIComponent(runId)}/artifact?kind=diff`,
      ),

    diffDownloadUrl: (runId) => {
      const params = new URLSearchParams({ kind: 'diff', download: '1' });
      const dlToken = getToken();
      if (dlToken) params.set('access_token', dlToken);
      return `${BASE}/runs/${encodeURIComponent(runId)}/artifact?${params}`;
    },

    getSystem: () => req<SystemInfo>('/system'),

    prewarmRunnerImage: () =>
      req<RunnerPrewarm>('/system/runner-image/prewarm', { method: 'POST' }),

    listModelProviders: async () =>
      (await req<{ providers: ModelProvider[] }>('/system/model-providers')).providers ?? [],
    createModelProvider: (input) =>
      req<ModelProvider>('/system/model-providers', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    updateModelProvider: (id, input) =>
      req<ModelProvider>(`/system/model-providers/${encodeURIComponent(id)}`, {
        method: 'PATCH',
        body: JSON.stringify(input),
      }),
    deleteModelProvider: (id) =>
      req<void>(`/system/model-providers/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    verifyModelProvider: (id) =>
      req<ModelProviderVerification>(`/system/model-providers/${encodeURIComponent(id)}/verify`, {
        method: 'POST',
      }),
    getModelProviderCatalog: async (id) =>
      (await req<{ models: CatalogModel[] }>(
        `/system/model-providers/${encodeURIComponent(id)}/catalog`,
      )).models ?? [],
    createProviderModel: (id, input) =>
      req<ProviderModel>(`/system/model-providers/${encodeURIComponent(id)}/models`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),

    // Project-owned model providers (M2). Same wire shapes as the cluster
    // catalog, scoped under /projects/{id}/model-providers (owner write / member
    // read); list/catalog use the same {providers}/{models} envelopes.
    listProjectModelProviders: async (projectId) =>
      (await req<{ providers: ModelProvider[] }>(
        `/projects/${encodeURIComponent(projectId)}/model-providers`,
      )).providers ?? [],
    createProjectModelProvider: (projectId, input) =>
      req<ModelProvider>(`/projects/${encodeURIComponent(projectId)}/model-providers`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    updateProjectModelProvider: (projectId, id, input) =>
      req<ModelProvider>(
        `/projects/${encodeURIComponent(projectId)}/model-providers/${encodeURIComponent(id)}`,
        { method: 'PATCH', body: JSON.stringify(input) },
      ),
    deleteProjectModelProvider: (projectId, id) =>
      req<void>(
        `/projects/${encodeURIComponent(projectId)}/model-providers/${encodeURIComponent(id)}`,
        { method: 'DELETE' },
      ),
    verifyProjectModelProvider: (projectId, id) =>
      req<ModelProviderVerification>(
        `/projects/${encodeURIComponent(projectId)}/model-providers/${encodeURIComponent(id)}/verify`,
        { method: 'POST' },
      ),
    getProjectModelProviderCatalog: async (projectId, id) =>
      (await req<{ models: CatalogModel[] }>(
        `/projects/${encodeURIComponent(projectId)}/model-providers/${encodeURIComponent(id)}/catalog`,
      )).models ?? [],
    createProjectProviderModel: (projectId, id, input) =>
      req<ProviderModel>(
        `/projects/${encodeURIComponent(projectId)}/model-providers/${encodeURIComponent(id)}/models`,
        { method: 'POST', body: JSON.stringify(input) },
      ),
    updateProjectProviderModel: (projectId, providerId, modelId, input) =>
      req<ProviderModel>(
        `/projects/${encodeURIComponent(projectId)}/model-providers/${encodeURIComponent(providerId)}/models/${encodeURIComponent(modelId)}`,
        { method: 'PATCH', body: JSON.stringify(input) },
      ),
    deleteProjectProviderModel: (projectId, providerId, modelId) =>
      req<void>(
        `/projects/${encodeURIComponent(projectId)}/model-providers/${encodeURIComponent(providerId)}/models/${encodeURIComponent(modelId)}`,
        { method: 'DELETE' },
      ),

    // Model catalog + account/project grants (D21).
    listModels: async () =>
      (await req<{ models: Model[] }>('/system/models')).models ?? [],
    listAccountModels: async () =>
      (await req<{ models: ProjectModel[] }>('/account/models')).models ?? [],
    createModel: (input) =>
      req<Model>('/system/models', { method: 'POST', body: JSON.stringify(input) }),
    updateModel: (id, input) =>
      req<Model>(`/system/models/${encodeURIComponent(id)}`, {
        method: 'PATCH',
        body: JSON.stringify(input),
      }),
    deleteModel: (id) =>
      req<void>(`/system/models/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    grantModel: (modelId, projectId) =>
      req<Model>(
        `/system/models/${encodeURIComponent(modelId)}/grants/${encodeURIComponent(projectId)}`,
        { method: 'PUT' },
      ),
    revokeModel: (modelId, projectId) =>
      req<Model>(
        `/system/models/${encodeURIComponent(modelId)}/grants/${encodeURIComponent(projectId)}`,
        { method: 'DELETE' },
      ),
    grantModelToAccount: (modelId, userId) =>
      req<Model>(
        `/system/models/${encodeURIComponent(modelId)}/account-grants/${encodeURIComponent(userId)}`,
        { method: 'PUT' },
      ),
    revokeModelFromAccount: (modelId, userId) =>
      req<Model>(
        `/system/models/${encodeURIComponent(modelId)}/account-grants/${encodeURIComponent(userId)}`,
        { method: 'DELETE' },
      ),
    listProjectModels: (projectId) =>
      req<ProjectModels>(`/projects/${encodeURIComponent(projectId)}/models`),

    // Kanban board embed (D31). The member+ board proxy: `board/links` gates the
    // header button + selector (reduced view, no credential fields); the
    // documents/* routes proxy jtype's document API with the effective token
    // resolved and applied server-side (never on the wire). The document
    // responses are jtype's native wire shapes, passed through verbatim.
    listProjectBoardLinks: async (projectId) =>
      (
        await req<{ links: BoardEmbedLink[] }>(
          `/projects/${encodeURIComponent(projectId)}/kanban/board/links`,
        )
      ).links ?? [],
    boardListDocuments: (projectId, workspaceId) =>
      req<JTypeDocumentListItem[]>(
        `/projects/${encodeURIComponent(projectId)}/kanban/board/documents?workspace=${encodeURIComponent(workspaceId)}`,
      ),
    boardGetDocument: (projectId, workspaceId, docId) =>
      req<JTypeCloudDocument>(
        `/projects/${encodeURIComponent(projectId)}/kanban/board/documents/${encodeURIComponent(docId)}?workspace=${encodeURIComponent(workspaceId)}`,
      ),
    boardSaveDocument: (projectId, workspaceId, body) =>
      req<JTypeSaveDocumentResponse>(
        `/projects/${encodeURIComponent(projectId)}/kanban/board/documents/save?workspace=${encodeURIComponent(workspaceId)}`,
        { method: 'POST', body: JSON.stringify(body) },
      ),

    // Project-scoped API keys (F12 / D24).
    listApiKeys: async (projectId) =>
      (
        await req<ApiKeysEnvelope>(`/projects/${encodeURIComponent(projectId)}/apikeys`)
      ).api_keys ?? [],
    createApiKey: (projectId, input) =>
      req<CreateApiKeyResponse>(`/projects/${encodeURIComponent(projectId)}/apikeys`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    revokeApiKey: async (projectId, keyId) => {
      await req<void>(
        `/projects/${encodeURIComponent(projectId)}/apikeys/${encodeURIComponent(keyId)}`,
        { method: 'DELETE' },
      );
    },

    // Services (blueprint §4).
    listRepositories: async () =>
      (await req<{ repositories: Service[] }>('/repositories')).repositories ?? [],
    listAccountRepositories: (q) =>
      req<AccountRepositoryCatalog>(`/account/repositories${q ? `?q=${encodeURIComponent(q)}` : ''}`),
    listAccountRepositoryBranches: async (provider, providerRepoId) =>
      (
        await req<{ branches: ServiceBranch[] }>(
          `/account/repositories/${encodeURIComponent(provider)}/${encodeURIComponent(providerRepoId)}/branches`,
        )
      ).branches ?? [],
    startAccountTask: (input) =>
      req<AccountTaskResponse>('/account/tasks', { method: 'POST', body: JSON.stringify(input) }),
    getRepository: (repositoryId) =>
      req<Service>(`/repositories/${encodeURIComponent(repositoryId)}`),
    listServices: async (projectId) =>
      (
        await req<ServicesEnvelope>(
          `/projects/${encodeURIComponent(projectId)}/repositories`,
        )
      ).services,

    createService: (projectId, input) =>
      req<Service>(`/projects/${encodeURIComponent(projectId)}/repositories`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),

    updateService: (serviceId, input) =>
      req<Service>(`/repositories/${encodeURIComponent(serviceId)}`, {
        method: 'PATCH',
        body: JSON.stringify(input),
      }),

    deleteService: (serviceId) =>
      req<void>(`/repositories/${encodeURIComponent(serviceId)}`, { method: 'DELETE' }),

    listProjectPlugins: async (projectId) =>
      (await req<{ plugins: ProjectPlugin[] }>(`/projects/${encodeURIComponent(projectId)}/plugins`)).plugins ?? [],
    startPluginInstall: (projectId, provider, input) =>
      req<PluginInstallStart>(`/projects/${encodeURIComponent(projectId)}/plugins/${encodeURIComponent(provider)}/connect`, {
        method: 'POST', body: JSON.stringify(input),
      }),
    listGitHubAppInstallations: async (projectId) =>
      (await req<{ installations: GitHubAppInstallation[] }>(`/projects/${encodeURIComponent(projectId)}/plugins/github/installations`)).installations ?? [],
    previewGitHubAppInstallationConsent: (projectId, installationId) =>
      req<GitHubInstallationConsentPreview>(`/projects/${encodeURIComponent(projectId)}/plugins/github/installations/${encodeURIComponent(installationId)}/consent`),
    selectGitHubAppInstallation: (projectId, installationId, input) =>
      req<ProjectPlugin>(`/projects/${encodeURIComponent(projectId)}/plugins/github/installations/${encodeURIComponent(installationId)}/select`, {
        method: 'POST', body: JSON.stringify(input),
      }),
    getJTypePluginConnectStatus: (projectId, installationId, connectId) =>
      req<JTypePluginConnectStatus>(`/projects/${encodeURIComponent(projectId)}/plugins/${encodeURIComponent(installationId)}/connect/${encodeURIComponent(connectId)}`),
    setProjectPluginEnabled: (installationId, enabled) =>
      req<ProjectPlugin>(`/plugins/${encodeURIComponent(installationId)}`, {
        method: 'PATCH', body: JSON.stringify({ status: enabled ? 'enabled' : 'disabled' }),
      }),
    setProjectPluginWorkspace: (installationId, workspaceId) =>
      req<ProjectPlugin>(`/plugins/${encodeURIComponent(installationId)}`, {
        method: 'PATCH', body: JSON.stringify({ workspace_id: workspaceId }),
      }),
    getProjectPluginImpact: (projectId, installationId) => req<{ services: number; automations: number }>(`/projects/${encodeURIComponent(projectId)}/plugins/${encodeURIComponent(installationId)}/impact`),
    listProjectPluginAudit: async (projectId, installationId) =>
      (await req<{ audit_events: PluginAuditEvent[] }>(`/projects/${encodeURIComponent(projectId)}/plugins/${encodeURIComponent(installationId)}/audit`)).audit_events ?? [],
    uninstallProjectPlugin: (installationId, force = false) => req<void>(`/plugins/${encodeURIComponent(installationId)}`, {
      method: 'DELETE',
      body: JSON.stringify({ confirmation: 'UNINSTALL', force }),
    }),
    listPluginRepositories: async (projectId, installationId, q) => (await req<{ repositories: PluginRepositoryResource[] }>(`/projects/${encodeURIComponent(projectId)}/plugins/${encodeURIComponent(installationId)}/repositories${q ? `?q=${encodeURIComponent(q)}` : ''}`)).repositories ?? [],
    listPluginWorkspaces: async (projectId, installationId) => (await req<{ workspaces: PluginWorkspaceResource[] }>(`/projects/${encodeURIComponent(projectId)}/plugins/${encodeURIComponent(installationId)}/workspaces`)).workspaces ?? [],
    listPluginBoards: async (projectId, installationId, workspaceId) => (await req<{ boards: PluginBoardResource[] }>(`/projects/${encodeURIComponent(projectId)}/plugins/${encodeURIComponent(installationId)}/boards${workspaceId ? `?workspace=${encodeURIComponent(workspaceId)}` : ''}`)).boards ?? [],
    getProviderCapabilities: (provider) => req<ScmProviderCapabilities>(`/providers/${encodeURIComponent(provider)}/capabilities`),
    listProjectAutomations: async (projectId) =>
      (await req<{ automations: ProjectAutomationSpec[] }>(`/projects/${encodeURIComponent(projectId)}/automations`)).automations ?? [],
    getProjectAutomation: (_projectId, automationId) => req<ProjectAutomationSpec>(`/automations/${encodeURIComponent(automationId)}`),
    createProjectAutomation: (projectId, input) =>
      req<ProjectAutomationSpec>(`/projects/${encodeURIComponent(projectId)}/automations`, { method: 'POST', body: JSON.stringify(input) }),
    updateProjectAutomation: (_projectId, automationId, input) =>
      req<ProjectAutomationSpec>(`/automations/${encodeURIComponent(automationId)}`, {
        method: 'PATCH', body: JSON.stringify(input),
      }),
    deleteProjectAutomation: (_projectId, automationId) =>
      req<void>(`/automations/${encodeURIComponent(automationId)}`, { method: 'DELETE' }),
    listAutomationExecutions: (automationId, before, state, limit = 20) => {
      const params = new URLSearchParams({ limit: String(limit) });
      if (before) params.set('before', before);
      if (state) params.set('state', state);
      return req<AutomationExecutionsPage>(`/automations/${encodeURIComponent(automationId)}/executions?${params.toString()}`);
    },
    getAutomationExecution: (automationId, executionId) =>
      req<AutomationExecution>(`/automations/${encodeURIComponent(automationId)}/executions/${encodeURIComponent(executionId)}`),
    runAutomationNow: (automationId, idempotencyKey) =>
      req<AutomationExecution>(`/automations/${encodeURIComponent(automationId)}/executions`, {
        method: 'POST', body: JSON.stringify({ idempotency_key: idempotencyKey }),
      }),
    getAutomationUsage: (automationId, from, to) => {
      const params = usageParams(from, to);
      return req<UsageSummary>(`/automations/${encodeURIComponent(automationId)}/usage${params}`);
    },
    getProjectUsage: (projectId, groupBy, from, to) => {
      const params = new URLSearchParams({ group_by: groupBy });
      if (from) params.set('from', from);
      if (to) params.set('to', to);
      return req<UsageSummaryEnvelope>(`/projects/${encodeURIComponent(projectId)}/usage?${params.toString()}`);
    },
    getAccountUsage: (groupBy, from, to) => {
      const params = new URLSearchParams({ group_by: groupBy });
      if (from) params.set('from', from);
      if (to) params.set('to', to);
      return req<UsageSummaryEnvelope>(`/account/usage?${params.toString()}`);
    },
    getServiceUsage: (serviceId, from, to) => {
      const params = usageParams(from, to);
      return req<UsageSummary>(`/repositories/${encodeURIComponent(serviceId)}/usage${params}`);
    },
    listModelPricingRevisions: async (modelId) =>
      (await req<{ pricing_revisions: ModelPricingRevision[] }>(
        `/system/models/${encodeURIComponent(modelId)}/pricing-revisions`,
      )).pricing_revisions ?? [],
    createModelPricingRevision: (modelId, input) =>
      req<ModelPricingRevision>(`/system/models/${encodeURIComponent(modelId)}/pricing-revisions`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    getServiceKanban: (serviceId) => req<ServiceKanbanBinding>(`/repositories/${encodeURIComponent(serviceId)}/agent-board`),
    getServiceKanbanPolicy: (serviceId) => req<ServiceKanbanPolicy>(`/repositories/${encodeURIComponent(serviceId)}/agent-board/policy`),
    listServiceKanbanCardExecutions: (serviceId, workspaceId, documentPath, before, limit = 20) => {
      const params = new URLSearchParams({
        workspace_id: workspaceId,
        document_path: documentPath,
        limit: String(limit),
      });
      if (before) params.set('before', before);
      return req<KanbanCardExecutionsPage>(
        `/repositories/${encodeURIComponent(serviceId)}/agent-board/card-executions?${params.toString()}`,
      );
    },
    createServiceKanbanOccurrence: (serviceId, input) => req<CreateKanbanCardOccurrenceResponse>(
      `/repositories/${encodeURIComponent(serviceId)}/agent-board/occurrences`,
      { method: 'POST', body: JSON.stringify(input) },
    ),
    putServiceKanban: (serviceId, input) => req<ServiceKanbanBinding>(`/repositories/${encodeURIComponent(serviceId)}/agent-board`, {
      method: 'PUT', body: JSON.stringify(input),
    }),
    deleteServiceKanban: (serviceId) => req<void>(`/repositories/${encodeURIComponent(serviceId)}/agent-board`, { method: 'DELETE' }),

    listServiceBranches: async (serviceId) =>
      (await req<{ branches: ServiceBranch[] }>(`/repositories/${encodeURIComponent(serviceId)}/branches`)).branches ?? [],

    createServiceRun: (serviceId, input) =>
      req<Run>(`/repositories/${encodeURIComponent(serviceId)}/runs`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    uploadRunAttachment: async (serviceId, file) => {
      const intent = await req<RunAttachmentIntent>(
        `/repositories/${encodeURIComponent(serviceId)}/attachments/intents`,
        {
          method: 'POST',
          body: JSON.stringify({
            name: file.name,
            content_type: file.type || 'application/octet-stream',
            size_bytes: file.size,
          }),
        },
      );
      const res = await fetch(intent.upload_url, {
        method: 'PUT',
        body: file,
        credentials: 'same-origin',
        headers: {
          'Content-Type': file.type || 'application/octet-stream',
          ...authHeaders(getToken()),
        },
      });
      if (res.status === 401) opts.onUnauthorized?.();
      if (!res.ok) return parseError(res);
      return intent;
    },

    listProviderRepos: async (provider, q) =>
      (
        await req<{ repos: ProviderRepo[] }>(
          `/providers/${encodeURIComponent(provider)}/repos${q ? `?q=${encodeURIComponent(q)}` : ''}`,
        )
      ).repos,

    // Members (blueprint §2).
    listMembers: async (projectId) =>
      (
        await req<MembersEnvelope>(
          `/projects/${encodeURIComponent(projectId)}/members`,
        )
      ).members,

    addMember: (projectId, input) =>
      req<Member>(`/projects/${encodeURIComponent(projectId)}/members`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),

    removeMember: async (projectId, userId) => {
      await req<void>(
        `/projects/${encodeURIComponent(projectId)}/members/${encodeURIComponent(userId)}`,
        { method: 'DELETE' },
      );
    },

    searchUsers: async (q) =>
      (await req<UsersEnvelope>(`/users?q=${encodeURIComponent(q)}`)).users,
  };
}

/* ------------------------------------------------------------------------- */

/**
 * The two auth-flow endpoints the AuthProvider needs live OUTSIDE the ApiClient
 * (the auth state machine sits above ApiProvider and cannot read the client).
 * They are plain same-origin fetches against /auth/* (not /api/v1).
 */

/** GET /auth/providers — configured OAuth providers (unauthenticated). */
export async function fetchAuthProviders(): Promise<AuthProviderInfo[]> {
  try {
    const res = await fetch('/auth/providers', {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
    });
    if (!res.ok) return [];
    const body = (await res.json()) as AuthProvidersEnvelope;
    return body.providers ?? [];
  } catch {
    // Orchestrator unreachable — the gate shows the setup guide; providers load
    // on the next reprobe.
    return [];
  }
}

/** POST /auth/logout — revoke the session + clear the cookie. Best-effort. */
export async function postLogout(token: string | undefined): Promise<void> {
  try {
    await fetch('/auth/logout', {
      method: 'POST',
      credentials: 'same-origin',
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
  } catch {
    /* network error — the local session is cleared regardless */
  }
}

/**
 * POST /auth/device/authorize — approve or deny a jcode device-code login
 * (docs/17 §3). Lives outside ApiClient (like the other /auth helpers): the
 * route is not under /api/v1 and auth rides the session cookie + optional
 * Bearer fallback. Throws ApiError on a typed failure (unknown/expired code,
 * already decided) so the page can show the server message verbatim.
 */
export async function postDeviceAuthorize(
  token: string | undefined,
  userCode: string,
  approve: boolean,
): Promise<{ status: string }> {
  const res = await fetch('/auth/device/authorize', {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      ...authHeaders(token),
    },
    body: JSON.stringify({ user_code: userCode, approve }),
  });
  if (!res.ok) return parseError(res);
  return res.json() as Promise<{ status: string }>;
}

export async function getDeviceAuthorizeState(
  token: string | undefined,
  userCode: string,
): Promise<{ status: string; device_id?: string }> {
  const params = new URLSearchParams({ user_code: userCode });
  const res = await fetch(`/auth/device/authorize?${params}`, {
    credentials: 'same-origin',
    headers: authHeaders(token),
  });
  if (!res.ok) return parseError(res);
  return res.json() as Promise<{ status: string; device_id?: string }>;
}
