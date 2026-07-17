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
  Automation,
  AutomationList,
  ApiKey,
  ApiKeysEnvelope,
  AuthProviderInfo,
  AuthProvidersEnvelope,
  BoardEmbedLink,
  CatalogModel,
  CreateApiKeyInput,
  CreateApiKeyResponse,
  CreateAutomationInput,
  CreateKanbanLinkInput,
  CreateProjectInput,
  CreateIntegrationInput,
  CreateModelInput,
  CreateModelProviderInput,
  CreateProviderModelInput,
  CreateRunInput,
  CreateScheduleInput,
  CreateServiceInput,
  EventsEnvelope,
  Integration,
  IntegrationsEnvelope,
  JtypeBoard,
  JtypeWorkspace,
  KanbanClusterConfig,
  KanbanConnectStart,
  KanbanConnectStatus,
  KanbanLink,
  Me,
  Member,
  MembersEnvelope,
  Model,
  ModelProvider,
  ModelProviderVerification,
  ProviderModel,
  PrInfo,
  Project,
  ProjectModels,
  ProjectsEnvelope,
  ProviderRepo,
  Run,
  RunArtifact,
  RunEvent,
  RunMessage,
  RunPermission,
  RunnerPrewarm,
  ResumeSessionOptions,
  RunsEnvelope,
  Schedule,
  Service,
  ServiceWebhookSetup,
  ServicesEnvelope,
  StreamFrame,
  SystemInfo,
  UpdateAutomationInput,
  UpdateIntegrationInput,
  UpdateKanbanConfigInput,
  UpdateModelInput,
  UpdateModelProviderInput,
  UpdateProjectInput,
  UpdateScheduleInput,
  UpdateServiceInput,
  UserSearchResult,
  UsersEnvelope,
} from './types';

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public body?: unknown,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

/**
 * The typed error `code` from the repo-standard `{ "error": { code, message } }`
 * envelope (11-api.md §0), or undefined for a non-ApiError / bodyless error. Used
 * to branch on codes the UI must treat specially (e.g. `jtype_oauth_unsupported`,
 * `connect_expired`) rather than string-matching the human message.
 */
export function apiErrorCode(err: unknown): string | undefined {
  if (err instanceof ApiError && err.body && typeof err.body === 'object') {
    const e = (err.body as { error?: { code?: string } }).error;
    if (e && typeof e === 'object' && typeof e.code === 'string') return e.code;
  }
  return undefined;
}

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
  /**
   * GET /api/v1/me — the current principal (user / identities / is_service).
   * 200 for every authenticated principal; only an unauthenticated request 401s.
   */
  getMe(): Promise<Me>;

  listProjects(): Promise<Project[]>;
  createProject(input: CreateProjectInput): Promise<Project>;
  getProject(id: string): Promise<Project>;
  /** PATCH /projects/{id} — only the provided fields are updated (11-api.md §2.1). */
  updateProject(id: string, input: UpdateProjectInput): Promise<Project>;
  /** DELETE /projects/{id} — cascades runs/events/artifacts; 204 No Content. */
  deleteProject(id: string): Promise<void>;

  listRuns(projectId: string): Promise<Run[]>;
  getRun(runId: string): Promise<Run>;
  cancelRun(runId: string): Promise<Run>;
  retryRun(runId: string): Promise<Run>;
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

  /** Replay events with seq > afterSeq (0 = from start). */
  listEvents(runId: string, afterSeq?: number): Promise<RunEvent[]>;
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

  /* ---- model catalog + project grants (D21) ----------------------------- */
  /** GET /api/v1/system/models — the whole catalog (cluster-admin). */
  listModels(): Promise<Model[]>;
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
  /**
   * GET /api/v1/projects/{id}/models — models granted to a project (member+).
   * Carries only id/name/model_name plus env_fallback; never a base_url or key.
   */
  listProjectModels(projectId: string): Promise<ProjectModels>;

  /* ---- kanban links (Feature E / F6) ------------------------------------ */
  /**
   * GET /api/v1/system/kanban/links — cluster-admin READ-ONLY overview of every
   * board→service binding across all projects (each carries project_id).
   */
  listKanbanLinks(): Promise<KanbanLink[]>;
  /** GET /api/v1/projects/{id}/kanban/links — a project's links (owner). */
  listProjectKanbanLinks(projectId: string): Promise<KanbanLink[]>;
  /**
   * POST /api/v1/projects/{id}/kanban/links — bind a board column to one of the
   * project's services (owner). `token` (optional, write-only) is the per-link
   * jtype PAT; omit to fall back to the cluster JTYPE_TOKEN.
   */
  createProjectKanbanLink(projectId: string, input: CreateKanbanLinkInput): Promise<KanbanLink>;
  /**
   * PATCH /api/v1/projects/{id}/kanban/links/{linkId} — rotate or clear ONLY the
   * link's per-link jtype token (owner; claims retained). "" clears back to the
   * cluster fallback; any other value rotates. Write-only, as on create.
   */
  updateProjectKanbanLinkToken(projectId: string, linkId: string, token: string): Promise<KanbanLink>;
  /** DELETE /api/v1/projects/{id}/kanban/links/{linkId} — remove a link (owner). */
  deleteProjectKanbanLink(projectId: string, linkId: string): Promise<void>;

  /* ---- kanban discovery pickers (D29) ----------------------------------- */
  /**
   * GET /api/v1/projects/{id}/kanban/jtype/workspaces — the caller's jtype
   * workspaces for the create-link workspace picker (owner). Uses the effective
   * token server-side (never serialized). 409 kanban_not_configured / 503
   * jtype_unreachable when the integration is off / unreachable (fail-visible —
   * the form falls back to manual entry); 400 jtype_unauthorized for a bad token.
   */
  listJtypeWorkspaces(projectId: string): Promise<JtypeWorkspace[]>;
  /**
   * GET /api/v1/projects/{id}/kanban/jtype/boards?workspace=<id> — the boards
   * (with columns) in a workspace for the board + column pickers (owner). Typed
   * errors mirror listJtypeWorkspaces.
   */
  listJtypeBoards(projectId: string, workspaceId: string): Promise<JtypeBoard[]>;

  /* ---- kanban board embed (D31) ----------------------------------------- */
  /**
   * GET /api/v1/projects/{id}/kanban/board/links — the reduced, member+ list of
   * the project's kanban links (no credential fields). Gates the "Kanban" header
   * button and feeds the board-embed modal's link selector. 403 for a viewer /
   * non-member (→ empty list → no button); this is NOT the owner-only
   * `listProjectKanbanLinks`.
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

  /* ---- cluster kanban config (D27) -------------------------------------- */
  /**
   * GET /api/v1/system/kanban — the cluster jtype config resolved DB › env › off
   * (cluster-admin). Never carries the token — only token_set / cluster_token_set.
   */
  getKanbanConfig(): Promise<KanbanClusterConfig>;
  /**
   * PUT /api/v1/system/kanban — set the DB override's base_url (required) and,
   * with three-state presence, its optional cluster fallback token (cluster-admin).
   * 400 for an invalid base_url; 409 cipher_not_configured for a token write with
   * no AUTH_TOKEN_KEY. Returns the resolved config (the GET shape).
   */
  updateKanbanConfig(input: UpdateKanbanConfigInput): Promise<KanbanClusterConfig>;
  /**
   * DELETE /api/v1/system/kanban — drop the DB override, falling back to env/off
   * (cluster-admin). Returns the new resolved config (the GET shape).
   */
  deleteKanbanConfig(): Promise<KanbanClusterConfig>;

  /* ---- kanban "Connect with jtype" device flow (D28) -------------------- */
  /**
   * POST /api/v1/system/kanban/connect — start a device flow for the CLUSTER
   * fallback token (cluster-admin). Requires a saved DB base_url (else 409
   * base_url_not_configured) and a configured cipher (else 409
   * cipher_not_configured); an old jtype without the OAuth routes yields 409
   * jtype_oauth_unsupported (fall back to pasting a token). The device_code is
   * withheld — poll with the returned connect_id.
   */
  startKanbanConnect(): Promise<KanbanConnectStart>;
  /**
   * GET /api/v1/system/kanban/connect/{connectID} — poll a cluster device flow.
   * On `complete` the token is already sealed into the cluster config and the
   * resolver invalidated. An unknown/expired connect_id is 404 connect_expired.
   */
  pollKanbanConnect(connectId: string): Promise<KanbanConnectStatus>;
  /**
   * POST /api/v1/projects/{id}/kanban/links/{linkID}/connect — start a device
   * flow for a PER-LINK token (owner). The link must already exist (create it
   * with a blank token first). 409 kanban_not_configured when the cluster
   * integration is off; 404 for a link that isn't this project's.
   */
  startLinkConnect(projectId: string, linkId: string): Promise<KanbanConnectStart>;
  /**
   * GET /api/v1/projects/{id}/kanban/links/{linkID}/connect/{connectID} — poll a
   * per-link device flow. On `complete` the token is sealed into the link's row
   * (credential_status flips to per_link). Unknown connect_id → 404 connect_expired.
   */
  pollLinkConnect(projectId: string, linkId: string, connectId: string): Promise<KanbanConnectStatus>;

  /* ---- schedules (F11 / D24) -------------------------------------------- */
  /** GET /api/v1/services/{id}/schedules — a service's cron triggers (member+). */
  listServiceSchedules(serviceId: string): Promise<Schedule[]>;
  /** POST /api/v1/services/{id}/schedules — create a cron trigger (owner). */
  createServiceSchedule(serviceId: string, input: CreateScheduleInput): Promise<Schedule>;
  /** PATCH /api/v1/schedules/{id} — edit cron_expr/prompt/enabled (owner). */
  updateSchedule(scheduleId: string, input: UpdateScheduleInput): Promise<Schedule>;
  /** DELETE /api/v1/schedules/{id} — remove a cron trigger (owner). */
  deleteSchedule(scheduleId: string): Promise<void>;

  /* ---- integrations (D19 / F5) ------------------------------------------ */
  /** GET /api/v1/projects/{id}/integrations — the project's integrations (member+). */
  listIntegrations(projectId: string): Promise<Integration[]>;
  /**
   * POST /api/v1/projects/{id}/integrations — add a git integration (owner). The
   * server verifies the token against the provider (400 integration_unreachable),
   * validates the host against the cluster allowlist (400 host_not_allowed), and
   * discovers bot_username. `token` is write-only.
   */
  createIntegration(projectId: string, input: CreateIntegrationInput): Promise<Integration>;
  /**
   * PATCH /api/v1/integrations/{id} — rename and/or rotate the token (owner). A
   * rotation re-verifies and refreshes bot_username. token is write-only.
   */
  updateIntegration(integrationId: string, input: UpdateIntegrationInput): Promise<Integration>;
  /** DELETE /api/v1/integrations/{id} — remove an integration (owner). Bound services unbind. */
  deleteIntegration(integrationId: string): Promise<void>;
  /**
   * GET /api/v1/projects/{id}/integrations/{iid}/repos?q= — repos the integration's
   * bot token can see (member+), for the service-onboarding repo picker.
   */
  listIntegrationRepos(projectId: string, integrationId: string, q?: string): Promise<ProviderRepo[]>;

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
  /** GET /api/v1/projects/{id}/services — the project's repo configurations. */
  listServices(projectId: string): Promise<Service[]>;
  /** POST /api/v1/projects/{id}/services — add a repository to a project. */
  createService(projectId: string, input: CreateServiceInput): Promise<Service>;
  /** PATCH /api/v1/services/{id} — edit a service (owner); default model (D21). */
  updateService(serviceId: string, input: UpdateServiceInput): Promise<Service>;
  /**
   * POST /api/v1/services/{id}/webhook — explicitly sync the provider's
   * @jcode comment webhook with the calling member's OAuth account. The API
   * never accepts or returns a provider token; typed 409/502 errors stay
   * visible to the Automation page.
   */
  ensureServiceWebhook(serviceId: string): Promise<ServiceWebhookSetup>;
  listServiceAutomations(serviceId: string): Promise<AutomationList>;
  createServiceAutomation(serviceId: string, input: CreateAutomationInput): Promise<Automation>;
  updateAutomation(automationId: string, input: UpdateAutomationInput): Promise<Automation>;
  deleteAutomation(automationId: string): Promise<void>;
  /** POST /api/v1/services/{id}/runs — dispatch a run against a specific service. */
  createServiceRun(serviceId: string, input: CreateRunInput): Promise<Run>;
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

/**
 * Token source: a static string (tests/legacy) or a getter (login gate) so the
 * client picks up runtime token changes without being rebuilt.
 */
export type TokenSource = string | undefined | (() => string | undefined);

export interface HttpClientOptions {
  /**
   * Fired on any 401 — the session-level "token was revoked/rotated" signal.
   * The auth layer clears the stored token and routes back to the login gate.
   */
  onUnauthorized?: () => void;
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

    retryRun: (runId) =>
      req<Run>(`/runs/${encodeURIComponent(runId)}/retry`, {
        method: 'POST',
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

    listEvents: async (runId, afterSeq = 0) =>
      (
        await req<EventsEnvelope>(
          `/runs/${encodeURIComponent(runId)}/events?after_seq=${afterSeq}`,
        )
      ).events,

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

    // Model catalog + project grants (D21).
    listModels: async () =>
      (await req<{ models: Model[] }>('/system/models')).models ?? [],
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
    listProjectModels: (projectId) =>
      req<ProjectModels>(`/projects/${encodeURIComponent(projectId)}/models`),

    // Kanban links (Feature E / F6). Management is project-scoped (owner); the
    // system list is a cluster-admin read-only overview.
    listKanbanLinks: async () =>
      (await req<{ links: KanbanLink[] }>('/system/kanban/links')).links ?? [],
    listProjectKanbanLinks: async (projectId) =>
      (await req<{ links: KanbanLink[] }>(`/projects/${encodeURIComponent(projectId)}/kanban/links`))
        .links ?? [],
    createProjectKanbanLink: (projectId, input) =>
      req<KanbanLink>(`/projects/${encodeURIComponent(projectId)}/kanban/links`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    updateProjectKanbanLinkToken: (projectId, linkId, token) =>
      req<KanbanLink>(
        `/projects/${encodeURIComponent(projectId)}/kanban/links/${encodeURIComponent(linkId)}`,
        { method: 'PATCH', body: JSON.stringify({ token }) },
      ),
    deleteProjectKanbanLink: (projectId, linkId) =>
      req<void>(
        `/projects/${encodeURIComponent(projectId)}/kanban/links/${encodeURIComponent(linkId)}`,
        { method: 'DELETE' },
      ),

    // Kanban discovery pickers (D29). The effective token is used server-side and
    // never crosses the wire — the responses carry only workspace/board metadata.
    listJtypeWorkspaces: async (projectId) =>
      (
        await req<{ workspaces: JtypeWorkspace[] }>(
          `/projects/${encodeURIComponent(projectId)}/kanban/jtype/workspaces`,
        )
      ).workspaces ?? [],
    listJtypeBoards: async (projectId, workspaceId) =>
      (
        await req<{ boards: JtypeBoard[] }>(
          `/projects/${encodeURIComponent(projectId)}/kanban/jtype/boards?workspace=${encodeURIComponent(workspaceId)}`,
        )
      ).boards ?? [],

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

    // Cluster kanban config (D27). Cluster-admin — set/clear the DB override that
    // supersedes the JTYPE_BASE_URL env fallback. The token is write-only.
    getKanbanConfig: () => req<KanbanClusterConfig>('/system/kanban'),
    updateKanbanConfig: (input) =>
      req<KanbanClusterConfig>('/system/kanban', {
        method: 'PUT',
        body: JSON.stringify(input),
      }),
    deleteKanbanConfig: () =>
      req<KanbanClusterConfig>('/system/kanban', { method: 'DELETE' }),

    // Kanban "Connect with jtype" device flow (D28). Start = POST (device_code
    // withheld); the console then polls the GET with the opaque connect_id while
    // the user authorises in jtype's browser page.
    startKanbanConnect: () =>
      req<KanbanConnectStart>('/system/kanban/connect', { method: 'POST' }),
    pollKanbanConnect: (connectId) =>
      req<KanbanConnectStatus>(`/system/kanban/connect/${encodeURIComponent(connectId)}`),
    startLinkConnect: (projectId, linkId) =>
      req<KanbanConnectStart>(
        `/projects/${encodeURIComponent(projectId)}/kanban/links/${encodeURIComponent(linkId)}/connect`,
        { method: 'POST' },
      ),
    pollLinkConnect: (projectId, linkId, connectId) =>
      req<KanbanConnectStatus>(
        `/projects/${encodeURIComponent(projectId)}/kanban/links/${encodeURIComponent(
          linkId,
        )}/connect/${encodeURIComponent(connectId)}`,
      ),

    // Schedules (F11 / D24). Listing is service-scoped (member+); management is
    // owner-only and keyed off the bare schedule id.
    listServiceSchedules: async (serviceId) =>
      (
        await req<{ schedules: Schedule[] }>(
          `/services/${encodeURIComponent(serviceId)}/schedules`,
        )
      ).schedules ?? [],
    createServiceSchedule: (serviceId, input) =>
      req<Schedule>(`/services/${encodeURIComponent(serviceId)}/schedules`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    updateSchedule: (scheduleId, input) =>
      req<Schedule>(`/schedules/${encodeURIComponent(scheduleId)}`, {
        method: 'PATCH',
        body: JSON.stringify(input),
      }),
    deleteSchedule: (scheduleId) =>
      req<void>(`/schedules/${encodeURIComponent(scheduleId)}`, { method: 'DELETE' }),

    // Integrations (D19 / F5).
    listIntegrations: async (projectId) =>
      (
        await req<IntegrationsEnvelope>(
          `/projects/${encodeURIComponent(projectId)}/integrations`,
        )
      ).integrations ?? [],
    createIntegration: (projectId, input) =>
      req<Integration>(`/projects/${encodeURIComponent(projectId)}/integrations`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    updateIntegration: (integrationId, input) =>
      req<Integration>(`/integrations/${encodeURIComponent(integrationId)}`, {
        method: 'PATCH',
        body: JSON.stringify(input),
      }),
    deleteIntegration: (integrationId) =>
      req<void>(`/integrations/${encodeURIComponent(integrationId)}`, { method: 'DELETE' }),
    listIntegrationRepos: async (projectId, integrationId, q) =>
      (
        await req<{ repos: ProviderRepo[] }>(
          `/projects/${encodeURIComponent(projectId)}/integrations/${encodeURIComponent(integrationId)}/repos${q ? `?q=${encodeURIComponent(q)}` : ''}`,
        )
      ).repos ?? [],

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
    listServices: async (projectId) =>
      (
        await req<ServicesEnvelope>(
          `/projects/${encodeURIComponent(projectId)}/services`,
        )
      ).services,

    createService: (projectId, input) =>
      req<Service>(`/projects/${encodeURIComponent(projectId)}/services`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),

    updateService: (serviceId, input) =>
      req<Service>(`/services/${encodeURIComponent(serviceId)}`, {
        method: 'PATCH',
        body: JSON.stringify(input),
      }),

    ensureServiceWebhook: (serviceId) =>
      req<ServiceWebhookSetup>(`/services/${encodeURIComponent(serviceId)}/webhook`, {
        method: 'POST',
      }),

    listServiceAutomations: (serviceId) =>
      req<AutomationList>(`/services/${encodeURIComponent(serviceId)}/automations`),
    createServiceAutomation: (serviceId, input) =>
      req<Automation>(`/services/${encodeURIComponent(serviceId)}/automations`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    updateAutomation: (automationId, input) =>
      req<Automation>(`/automations/${encodeURIComponent(automationId)}`, {
        method: 'PATCH',
        body: JSON.stringify(input),
      }),
    deleteAutomation: (automationId) =>
      req<void>(`/automations/${encodeURIComponent(automationId)}`, { method: 'DELETE' }),

    createServiceRun: (serviceId, input) =>
      req<Run>(`/services/${encodeURIComponent(serviceId)}/runs`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),

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
