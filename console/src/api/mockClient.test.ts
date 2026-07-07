import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMockClient } from './mockClient';
import { ApiError, type ApiClient } from './client';
import { initialEventState, reduceEvents } from './eventReducer';
import type { RunEvent } from './types';

// The mock uses setTimeout for both request latency and playback. Fake timers
// let us drive the whole lifecycle deterministically.
beforeEach(() => {
  vi.useFakeTimers();
});

async function flush(ms: number) {
  await vi.advanceTimersByTimeAsync(ms);
}

async function makeProjectAndRun(
  client: ApiClient,
  prompt = 'Add a line Hello to README',
) {
  const projectP = client.createProject({
    name: 'demo',
    repo_url: 'https://gitea.local/acme/demo.git',
    default_branch: 'main',
  });
  await flush(500);
  const project = await projectP;

  const runP = client.createRun(project.id, { prompt });
  await flush(500);
  const run = await runP;
  return { project, run };
}

describe('mockClient — lifecycle', () => {
  it('drives a happy-path run to succeeded and produces a diff', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);
    expect(run.status).toBe('queued');

    // Advance past the full happy-path playback (~6s scripted).
    await flush(8000);

    const finalP = client.getRun(run.id);
    await flush(200);
    const final = await finalP;
    expect(final.status).toBe('succeeded');
    expect(final.started_at).toBeTruthy();
    expect(final.finished_at).toBeTruthy();

    const diffP = client.getDiff(run.id);
    await flush(200);
    const diff = await diffP;
    expect(diff.kind).toBe('diff');
    expect(diff.content).toContain('Hello');
  });

  it('fails at clone when the prompt says "fail" with a readable reason', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client, 'please fail this run');
    await flush(8000);

    const finalP = client.getRun(run.id);
    await flush(200);
    const final = await finalP;

    expect(final.status).toBe('failed');
    expect(final.failure_reason).toBe('clone_failed');
    expect(final.failure_message).toBeTruthy();
    expect(final.failure_message!.length).toBeGreaterThan(10);
  });

  it('retry creates a new run linked via retried_from', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client, 'fail me');
    await flush(8000);

    const retryP = client.retryRun(run.id);
    await flush(500);
    const retried = await retryP;

    expect(retried.id).not.toBe(run.id);
    expect(retried.retried_from).toBe(run.id);
  });

  it('cancel stops a non-terminal run', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);
    await flush(1300); // now running

    const cancelP = client.cancelRun(run.id);
    await flush(300);
    const canceled = await cancelP;
    expect(canceled.status).toBe('canceled');
    expect(canceled.finished_at).toBeTruthy();
  });
});

// Contract alignment (11-api.md §2.2): cancel on a terminal run → 409, retry on
// a non-terminal run → 409, retry sets attempt = orig + 1. The mock must throw
// the same 409s as the HTTP client so demo/e2e exercise real conflict handling.
describe('mockClient — cancel/retry conflict semantics (409)', () => {
  it('cancel on a terminal (succeeded) run throws 409 conflict', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);
    await flush(8000); // drive to succeeded

    const p = client.cancelRun(run.id).then(
      () => ({ ok: true as const }),
      (e) => ({ ok: false as const, err: e }),
    );
    await flush(300);
    const res = await p;
    expect(res.ok).toBe(false);
    if (!res.ok) {
      expect(res.err).toBeInstanceOf(ApiError);
      expect((res.err as ApiError).status).toBe(409);
    }
  });

  it('retry on a non-terminal (running) run throws 409 conflict', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);
    await flush(1300); // now running (non-terminal)

    const p = client.retryRun(run.id).then(
      () => ({ ok: true as const }),
      (e) => ({ ok: false as const, err: e }),
    );
    await flush(300);
    const res = await p;
    expect(res.ok).toBe(false);
    if (!res.ok) {
      expect(res.err).toBeInstanceOf(ApiError);
      expect((res.err as ApiError).status).toBe(409);
    }
  });

  it('retry of a terminal run increments attempt (orig + 1)', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client, 'please fail this run');
    await flush(8000); // failed → terminal

    const origP = client.getRun(run.id);
    await flush(200);
    const orig = await origP;
    expect(orig.attempt).toBe(1);

    const retryP = client.retryRun(run.id);
    await flush(500);
    const retried = await retryP;
    expect(retried.attempt).toBe(2);
    expect(retried.retried_from).toBe(run.id);
  });
});

// Git integration + project CRUD (F3/F4). The mock must mirror the orchestrator
// contract (11-api.md §2.1) so demo/e2e hit the same payload shape and 400s.
describe('mockClient — project git integration + CRUD', () => {
  it('stores git integration fields when creating a draft_pr project', async () => {
    const client = createMockClient();
    const p = client.createProject({
      name: 'seed',
      repo_url: 'https://gitea.local/jcloud/seed.git',
      default_branch: 'main',
      git_mode: 'draft_pr',
      provider: 'gitea',
      provider_url: 'http://gitea.internal:3000',
      provider_repo: 'jcloud/seed',
    });
    await flush(200);
    const project = await p;
    expect(project.git_mode).toBe('draft_pr');
    expect(project.provider).toBe('gitea');
    expect(project.provider_repo).toBe('jcloud/seed');
    expect(project.provider_url).toBe('http://gitea.internal:3000');
  });

  it('defaults a readonly project to empty provider fields', async () => {
    const client = createMockClient();
    const p = client.createProject({
      name: 'demo',
      repo_url: 'https://gitea.local/acme/demo.git',
      default_branch: 'main',
    });
    await flush(200);
    const project = await p;
    expect(project.git_mode).toBe('readonly');
    expect(project.provider_repo).toBe('');
  });

  it('rejects draft_pr without provider_repo (400 bad_request)', async () => {
    const client = createMockClient();
    const p = client
      .createProject({
        name: 'seed',
        repo_url: 'https://gitea.local/jcloud/seed.git',
        default_branch: 'main',
        git_mode: 'draft_pr',
      })
      .then(
        () => ({ ok: true as const }),
        (e) => ({ ok: false as const, err: e }),
      );
    await flush(200);
    const res = await p;
    expect(res.ok).toBe(false);
    if (!res.ok) {
      expect(res.err).toBeInstanceOf(ApiError);
      expect((res.err as ApiError).status).toBe(400);
    }
  });

  it('PATCH updates default_branch + flips git_mode to draft_pr', async () => {
    const client = createMockClient();
    const createP = client.createProject({
      name: 'demo',
      repo_url: 'https://gitea.local/acme/demo.git',
      default_branch: 'main',
    });
    await flush(200);
    const created = await createP;

    const patchP = client.updateProject(created.id, {
      default_branch: 'dev',
      git_mode: 'draft_pr',
      provider: 'gitea',
      provider_repo: 'acme/demo',
    });
    await flush(200);
    const updated = await patchP;
    expect(updated.default_branch).toBe('dev');
    expect(updated.git_mode).toBe('draft_pr');
    expect(updated.provider_repo).toBe('acme/demo');

    // Persisted: a subsequent GET reflects the patch.
    const getP = client.getProject(created.id);
    await flush(200);
    expect((await getP).default_branch).toBe('dev');
  });

  it('DELETE removes the project and cascades its runs', async () => {
    const client = createMockClient();
    const { project, run } = await makeProjectAndRun(client);

    const delP = client.deleteProject(project.id);
    await flush(200);
    await delP;

    const getProjectP = client.getProject(project.id).then(
      () => ({ ok: true as const }),
      (e) => ({ ok: false as const, err: e }),
    );
    await flush(200);
    const res = await getProjectP;
    expect(res.ok).toBe(false);
    if (!res.ok) expect((res.err as ApiError).status).toBe(404);

    // The run was cascaded away too.
    const getRunP = client.getRun(run.id).then(
      () => ({ ok: true as const }),
      (e) => ({ ok: false as const, err: e }),
    );
    await flush(200);
    const runRes = await getRunP;
    expect(runRes.ok).toBe(false);
  });

  it('DELETE of a missing project throws 404', async () => {
    const client = createMockClient();
    const p = client.deleteProject('nope').then(
      () => ({ ok: true as const }),
      (e) => ({ ok: false as const, err: e }),
    );
    await flush(200);
    const res = await p;
    expect(res.ok).toBe(false);
    if (!res.ok) expect((res.err as ApiError).status).toBe(404);
  });
});

describe('mockClient — streaming (replay-then-live) into the reducer', () => {
  it('replays backlog then follows live, feeding a correctly ordered timeline', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);

    // Let some events accrue (this becomes the "backlog").
    await flush(3000);

    // 1. Replay via listEvents (what the hook does on mount).
    const backlogP = client.listEvents(run.id, 0);
    await flush(200);
    const backlog = await backlogP;
    expect(backlog.length).toBeGreaterThan(0);

    let state = reduceEvents(initialEventState(), backlog);
    const backlogLastSeq = state.lastSeq;

    // 2. Open the stream from our cursor; collect frames.
    const framesLive: RunEvent[] = [];
    const handle = client.streamRun(run.id, state.lastSeq, {
      onFrame: (f) => framesLive.push(f.data),
    });

    // Drive the rest of the run to completion.
    await flush(8000);

    for (const e of framesLive) {
      state = reduceEvents(state, e);
    }
    handle.close();

    // Timeline is strictly increasing and deduped.
    const seqs = state.events.map((e) => e.seq);
    const sorted = [...seqs].sort((a, b) => a - b);
    expect(seqs).toEqual(sorted);
    expect(new Set(seqs).size).toBe(seqs.length);

    // Live frames only carried NEW seqs (> backlog cursor).
    expect(framesLive.every((e) => e.seq > backlogLastSeq)).toBe(true);

    // Ends in a terminal status.
    expect(state.derivedStatus).toBe('succeeded');
  });

  it('a fresh stream from seq 0 replays the entire history (refresh scenario)', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);
    await flush(8000); // run fully complete

    const frames: RunEvent[] = [];
    const handle = client.streamRun(run.id, 0, {
      onFrame: (f) => frames.push(f.data),
    });
    await flush(50);
    handle.close();

    const state = frames.reduce(
      (acc, e) => reduceEvents(acc, e),
      initialEventState(),
    );
    // Full replay yields the same terminal status as the live run.
    expect(state.derivedStatus).toBe('succeeded');
    expect(state.events.length).toBe(frames.length);
    expect(state.events.length).toBeGreaterThan(3);
  });

  it('closing the stream detaches the live subscription', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);

    const frames: RunEvent[] = [];
    const handle = client.streamRun(run.id, 0, {
      onFrame: (f) => frames.push(f.data),
    });
    await flush(1500);
    const countAtClose = frames.length;
    handle.close();

    // Further playback must not push to a closed subscriber.
    await flush(8000);
    expect(frames.length).toBe(countAtClose);
  });
});

describe('mockClient — identity / services / members (M4 demo parity)', () => {
  it('returns a signed-in cluster-admin from getMe', async () => {
    const client = createMockClient();
    const p = client.getMe();
    await flush(200);
    const me = await p;
    expect(me.user.is_cluster_admin).toBe(true);
    expect(me.identities.length).toBeGreaterThan(0);
  });

  it('a new project has a default service, and add-repository grows the list', async () => {
    const client = createMockClient();
    const projP = client.createProject({
      name: 'demo',
      repo_url: 'https://github.com/acme/demo',
      default_branch: 'main',
    });
    await flush(200);
    const project = await projP;
    expect(project.role).toBe('owner');
    expect(project.services).toHaveLength(1);

    const svcP = client.createService(project.id, {
      name: 'web',
      repo_url: 'https://github.com/acme/web',
      git_mode: 'readonly',
    });
    await flush(200);
    await svcP;

    const listP = client.listServices(project.id);
    await flush(200);
    expect(await listP).toHaveLength(2);
  });

  it('seeds the creator as owner, adds a member by search and removes them', async () => {
    const client = createMockClient();
    const projP = client.createProject({
      name: 'demo',
      repo_url: 'https://github.com/acme/demo',
      default_branch: 'main',
    });
    await flush(200);
    const project = await projP;

    const m0P = client.listMembers(project.id);
    await flush(200);
    expect(await m0P).toHaveLength(1); // creator (owner)

    const searchP = client.searchUsers('grace');
    await flush(200);
    const found = await searchP;
    expect(found.length).toBeGreaterThan(0);

    const addP = client.addMember(project.id, { user_id: found[0]!.id, role: 'viewer' });
    await flush(200);
    await addP;

    const m1P = client.listMembers(project.id);
    await flush(200);
    expect(await m1P).toHaveLength(2);

    const rmP = client.removeMember(project.id, found[0]!.id);
    await flush(200);
    await rmP;

    const m2P = client.listMembers(project.id);
    await flush(200);
    expect(await m2P).toHaveLength(1);
  });

  it('getSystem carries the auth block (providers + users_count)', async () => {
    const client = createMockClient();
    const p = client.getSystem();
    await flush(200);
    const sys = await p;
    expect(sys.auth?.providers).toContain('gitea');
    expect(sys.auth?.users_count).toBeGreaterThan(0);
  });
});

describe('mockClient — getSystem (cluster snapshot)', () => {
  it('returns a plausible snapshot with no secrets and derives capacity from live runs', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);

    // While the run is scheduling/running, capacity reflects it.
    await flush(1500); // past scheduling(400ms)+running(1200ms) transitions
    const runningP = client.getRun(run.id);
    await flush(200);
    const live = await runningP;
    expect(['scheduling', 'running']).toContain(live.status);

    const sysP = client.getSystem();
    await flush(200);
    const sys = await sysP;

    expect(sys.capacity.max_concurrent_runs).toBeGreaterThan(0);
    // Exactly one non-terminal run exists, counted in one of the active buckets.
    const active =
      sys.capacity.running + sys.capacity.scheduling + sys.capacity.queued;
    expect(active).toBeGreaterThanOrEqual(1);
    expect(sys.guardrails.run_timeout_seconds).toBeGreaterThan(0);
    expect(sys.runner.image).toBeTruthy();
    expect(sys.namespace).toBeTruthy();

    // No secret shape may appear anywhere in the serialized snapshot.
    const raw = JSON.stringify(sys).toLowerCase();
    expect(raw).not.toContain('token');
    expect(raw).not.toContain('secret');
    expect(raw).not.toContain('password');
  });

  it('flips gitea_enabled when a draft_pr project exists', async () => {
    const client = createMockClient();

    const p1 = client.getSystem();
    await flush(200);
    expect((await p1).provider.gitea_enabled).toBe(false);

    const projP = client.createProject({
      name: 'pr',
      repo_url: 'https://gitea.local/o/r.git',
      default_branch: 'main',
      git_mode: 'draft_pr',
      provider: 'gitea',
      provider_repo: 'o/r',
    });
    await flush(300);
    await projP;

    const p2 = client.getSystem();
    await flush(200);
    expect((await p2).provider.gitea_enabled).toBe(true);
  });
});
