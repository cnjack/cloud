import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, createHttpClient } from './client';

interface FetchCall {
  url: string;
  init: RequestInit | undefined;
}

function mockFetch(
  handler: (call: FetchCall) => { status?: number; body?: unknown; text?: string },
) {
  const calls: FetchCall[] = [];
  const fn = vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const { status = 200, body, text } = handler({ url, init });
    const payload = text ?? (body === undefined ? '' : JSON.stringify(body));
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: `S${status}`,
      json: async () => JSON.parse(payload),
      text: async () => payload,
    } as unknown as Response;
  });
  vi.stubGlobal('fetch', fn);
  return { calls };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('httpClient — request shaping', () => {
  it('hits /api/v1/projects, attaches the bearer token, and unwraps the envelope', async () => {
    const { calls } = mockFetch(() => ({
      body: { projects: [{ id: 'p1', name: 'demo' }] },
    }));
    const client = createHttpClient('secret-token');
    const projects = await client.listProjects();

    expect(calls[0]!.url).toBe('/api/v1/projects');
    const headers = calls[0]!.init!.headers as Record<string, string>;
    expect(headers.Authorization).toBe('Bearer secret-token');
    // Envelope { projects: [...] } is unwrapped to a bare array.
    expect(projects).toHaveLength(1);
    expect(projects[0]!.id).toBe('p1');
  });

  it('omits Authorization when no token is configured', async () => {
    const { calls } = mockFetch(() => ({ body: { projects: [] } }));
    const client = createHttpClient(undefined);
    await client.listProjects();
    const headers = calls[0]!.init!.headers as Record<string, string>;
    expect(headers.Authorization).toBeUndefined();
  });

  it('POSTs create-project with a JSON body and content-type', async () => {
    const { calls } = mockFetch(({ init }) => ({
      status: 201,
      body: JSON.parse(init!.body as string),
    }));
    const client = createHttpClient('t');
    const project = await client.createProject({
      name: 'demo',
      repo_url: 'https://git/demo.git',
      default_branch: 'main',
    });

    expect(calls[0]!.init!.method).toBe('POST');
    const headers = calls[0]!.init!.headers as Record<string, string>;
    expect(headers['Content-Type']).toBe('application/json');
    expect(project.name).toBe('demo');
  });

  it('lists runs via the project-scoped route and unwraps the envelope', async () => {
    const { calls } = mockFetch(() => ({
      body: { runs: [{ id: 'r1', status: 'running' }] },
    }));
    const client = createHttpClient('t');
    const runs = await client.listRuns('proj 1/x');
    expect(calls[0]!.url).toBe('/api/v1/projects/proj%201%2Fx/runs');
    expect(runs).toHaveLength(1);
    expect(runs[0]!.id).toBe('r1');
  });

  it('builds the events URL with after_seq and unwraps the envelope', async () => {
    const { calls } = mockFetch(() => ({
      body: { events: [{ seq: 8, ts: '', type: 'agent.text', payload: {} }] },
    }));
    const client = createHttpClient('t');
    const events = await client.listEvents('run1', 7);
    expect(calls[0]!.url).toBe('/api/v1/runs/run1/events?after_seq=7');
    expect(events[0]!.seq).toBe(8);
  });

  it('creates a run under the project path', async () => {
    const { calls } = mockFetch(({ init }) => ({
      status: 201,
      body: { id: 'run_x', ...JSON.parse(init!.body as string) },
    }));
    const client = createHttpClient('t');
    await client.createRun('p1', { prompt: 'do it' });
    expect(calls[0]!.url).toBe('/api/v1/projects/p1/runs');
    expect(calls[0]!.init!.method).toBe('POST');
  });

  it('POSTs to cancel and retry endpoints', async () => {
    const { calls } = mockFetch(() => ({ body: { id: 'r', status: 'canceled' } }));
    const client = createHttpClient('t');
    await client.cancelRun('r1');
    await client.retryRun('r1');
    expect(calls[0]!.url).toBe('/api/v1/runs/r1/cancel');
    expect(calls[1]!.url).toBe('/api/v1/runs/r1/retry');
    expect(calls.every((c) => c.init!.method === 'POST')).toBe(true);
  });
});

describe('httpClient — error handling', () => {
  it('throws ApiError with message from the nested error envelope', async () => {
    // 11-api.md §0: { error: { code, message } }.
    mockFetch(() => ({
      status: 404,
      body: { error: { code: 'not_found', message: 'run not found' } },
    }));
    const client = createHttpClient('t');
    await expect(client.getRun('nope')).rejects.toMatchObject({
      name: 'ApiError',
      status: 404,
      message: 'run not found',
    });
  });

  it('still tolerates a flat string error body', async () => {
    mockFetch(() => ({ status: 400, body: { error: 'bad input' } }));
    const client = createHttpClient('t');
    await expect(client.getRun('x')).rejects.toMatchObject({
      message: 'bad input',
    });
  });

  it('falls back to text for non-JSON error bodies', async () => {
    mockFetch(() => ({ status: 500, text: 'boom' }));
    const client = createHttpClient('t');
    await expect(client.listProjects()).rejects.toBeInstanceOf(ApiError);
  });
});

describe('httpClient — artifact', () => {
  it('fetches the diff from the singular artifact route with kind=diff', async () => {
    const { calls } = mockFetch(() => ({
      body: { run_id: 'r', kind: 'diff', content: 'x', created_at: '' },
    }));
    const client = createHttpClient('t');
    await client.getDiff('run9');
    expect(calls[0]!.url).toBe('/api/v1/runs/run9/artifact?kind=diff');
  });

  it('builds a download url with kind, download and access_token', () => {
    const client = createHttpClient('tok');
    const url = client.diffDownloadUrl('run9');
    expect(url).toContain('/api/v1/runs/run9/artifact?');
    expect(url).toContain('kind=diff');
    expect(url).toContain('download=1');
    expect(url).toContain('access_token=tok');
  });
});
