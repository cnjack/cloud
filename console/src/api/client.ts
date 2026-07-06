/*
 * client.ts — the ApiClient interface + the real HTTP implementation.
 *
 * Both the HTTP client and the in-memory mock (mockClient.ts) implement
 * ApiClient, so the whole app is demo-able / e2e-testable without a cluster by
 * flipping VITE_DEMO=1. This is the ONE module that knows the wire format; if
 * 11-api.md drifts from the fallback route spec, reconcile it here.
 */
import type {
  CreateProjectInput,
  CreateRunInput,
  EventsEnvelope,
  Project,
  ProjectsEnvelope,
  Run,
  RunArtifact,
  RunEvent,
  RunsEnvelope,
  StreamFrame,
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
  listProjects(): Promise<Project[]>;
  createProject(input: CreateProjectInput): Promise<Project>;
  getProject(id: string): Promise<Project>;

  listRuns(projectId: string): Promise<Run[]>;
  createRun(projectId: string, input: CreateRunInput): Promise<Run>;
  getRun(runId: string): Promise<Run>;
  cancelRun(runId: string): Promise<Run>;
  retryRun(runId: string): Promise<Run>;

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

export function createHttpClient(token: string | undefined): ApiClient {
  async function req<T>(
    path: string,
    init?: RequestInit,
  ): Promise<T> {
    const res = await fetch(`${BASE}${path}`, {
      ...init,
      headers: {
        Accept: 'application/json',
        ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
        ...authHeaders(token),
        ...init?.headers,
      },
    });
    if (!res.ok) return parseError(res);
    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }

  return {
    // Lists are wrapped in envelopes (11-api.md §2); unwrap to bare arrays.
    listProjects: async () =>
      (await req<ProjectsEnvelope>('/projects')).projects,

    createProject: (input) =>
      req<Project>('/projects', {
        method: 'POST',
        body: JSON.stringify(input),
      }),

    getProject: (id) => req<Project>(`/projects/${encodeURIComponent(id)}`),

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
      if (token) params.set('access_token', token);
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
      if (token) params.set('access_token', token);
      return `${BASE}/runs/${encodeURIComponent(runId)}/artifact?${params}`;
    },
  };
}
