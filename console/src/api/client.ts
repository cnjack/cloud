/*
 * client.ts — the ApiClient interface + the real HTTP implementation.
 *
 * Both the HTTP client and the in-memory mock (mockClient.ts) implement
 * ApiClient, so the whole app is demo-able / e2e-testable without a cluster by
 * flipping VITE_DEMO=1. This is the ONE module that knows the wire format; if
 * 11-api.md drifts from the fallback route spec, reconcile it here.
 */
import type {
  AddMemberInput,
  AuthProviderInfo,
  AuthProvidersEnvelope,
  CreateProjectInput,
  CreateRunInput,
  CreateServiceInput,
  EventsEnvelope,
  Me,
  Member,
  MembersEnvelope,
  PrInfo,
  Project,
  ProjectsEnvelope,
  Run,
  RunArtifact,
  RunEvent,
  RunsEnvelope,
  Service,
  ServicesEnvelope,
  StreamFrame,
  SystemInfo,
  UpdateProjectInput,
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
  createRun(projectId: string, input: CreateRunInput): Promise<Run>;
  getRun(runId: string): Promise<Run>;
  cancelRun(runId: string): Promise<Run>;
  retryRun(runId: string): Promise<Run>;

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

  /* ---- services (blueprint §4) ------------------------------------------- */
  /** GET /api/v1/projects/{id}/services — the project's repo configurations. */
  listServices(projectId: string): Promise<Service[]>;
  /** POST /api/v1/projects/{id}/services — add a repository to a project. */
  createService(projectId: string, input: CreateServiceInput): Promise<Service>;
  /** POST /api/v1/services/{id}/runs — dispatch a run against a specific service. */
  createServiceRun(serviceId: string, input: CreateRunInput): Promise<Run>;

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

    createRun: (projectId, input) =>
      req<Run>(`/projects/${encodeURIComponent(projectId)}/runs`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),

    getRun: (runId) => req<Run>(`/runs/${encodeURIComponent(runId)}`),

    cancelRun: (runId) =>
      req<Run>(`/runs/${encodeURIComponent(runId)}/cancel`, {
        method: 'POST',
      }),

    retryRun: (runId) =>
      req<Run>(`/runs/${encodeURIComponent(runId)}/retry`, {
        method: 'POST',
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

    createServiceRun: (serviceId, input) =>
      req<Run>(`/services/${encodeURIComponent(serviceId)}/runs`, {
        method: 'POST',
        body: JSON.stringify(input),
      }),

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
