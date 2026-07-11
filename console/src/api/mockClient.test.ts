import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMockClient } from './mockClient';
import { ApiError, apiErrorCode, type ApiClient } from './client';
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

// A project is a pure container now: create it empty, attach a 'default'
// service (the repo config), then dispatch the run against that service.
async function makeProjectAndRun(
  client: ApiClient,
  prompt = 'Add a line Hello to README',
) {
  const projectP = client.createProject({ name: 'demo' });
  await flush(500);
  const project = await projectP;

  const serviceP = client.createService(project.id, {
    name: 'default',
    repo_url: 'https://gitea.local/acme/demo.git',
    default_branch: 'main',
  });
  await flush(500);
  const service = await serviceP;

  const runP = client.createServiceRun(service.id, { prompt });
  await flush(500);
  const run = await runP;
  return { project, service, run };
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

// Git integration (now on services) + project CRUD (F3/F4). The mock must
// mirror the orchestrator contract so demo/e2e hit the same payload shape and
// 400s: repo config lives ONLY on services; a project PATCH is a rename.
describe('mockClient — service git integration + project CRUD', () => {
  async function makeProject(client: ApiClient, name = 'demo') {
    const p = client.createProject({ name });
    await flush(200);
    return p;
  }

  it('stores git integration fields when creating a draft_pr service', async () => {
    const client = createMockClient();
    const project = await makeProject(client, 'seed');
    const p = client.createService(project.id, {
      name: 'default',
      repo_url: 'https://gitea.local/jcloud/seed.git',
      default_branch: 'main',
      git_mode: 'draft_pr',
    });
    await flush(200);
    const svc = await p;
    expect(svc.git_mode).toBe('draft_pr');
    expect(svc.provider).toBe('gitea');
    expect(svc.repo_kind).toBe('provider');
    expect(svc.repo_owner_name).toBe('jcloud/seed');
  });

  it('defaults a new service to readonly on the main branch', async () => {
    const client = createMockClient();
    const project = await makeProject(client);
    const p = client.createService(project.id, {
      repo_url: 'https://gitea.local/acme/demo.git',
    });
    await flush(200);
    const svc = await p;
    expect(svc.git_mode).toBe('readonly');
    expect(svc.default_branch).toBe('main');
    expect(svc.name).toBe('default');
  });

  it('classifies a non-provider repo URL as raw (no provider fields)', async () => {
    const client = createMockClient();
    const project = await makeProject(client);
    const p = client.createService(project.id, {
      repo_url: 'git://seed.internal/seed.git',
    });
    await flush(200);
    const svc = await p;
    expect(svc.repo_kind).toBe('raw');
    expect(svc.provider).toBeUndefined();
    expect(svc.repo_owner_name).toBeUndefined();
    expect(svc.raw_repo_url).toBe('git://seed.internal/seed.git');
  });

  it('rejects a draft_pr service on a raw (non-provider) repo (400 bad_request)', async () => {
    const client = createMockClient();
    const project = await makeProject(client, 'seed');
    const p = client
      .createService(project.id, {
        repo_url: 'git://seed.internal/seed.git',
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

  it('PATCH renames the project (the only project-level edit)', async () => {
    const client = createMockClient();
    const created = await makeProject(client);

    const patchP = client.updateProject(created.id, { name: 'renamed' });
    await flush(200);
    const updated = await patchP;
    expect(updated.name).toBe('renamed');

    // Persisted: a subsequent GET reflects the patch.
    const getP = client.getProject(created.id);
    await flush(200);
    expect((await getP).name).toBe('renamed');
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

  it('a new project starts with no services, and add-repository grows the list', async () => {
    const client = createMockClient();
    const projP = client.createProject({ name: 'demo' });
    await flush(200);
    const project = await projP;
    expect(project.role).toBe('owner');
    expect(project.services).toHaveLength(0);

    const defaultP = client.createService(project.id, {
      name: 'default',
      repo_url: 'https://github.com/acme/demo',
      git_mode: 'readonly',
    });
    await flush(200);
    await defaultP;

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
    const projP = client.createProject({ name: 'demo' });
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

  it('flips gitea_enabled when a draft_pr service exists', async () => {
    const client = createMockClient();

    const p1 = client.getSystem();
    await flush(200);
    expect((await p1).provider.gitea_enabled).toBe(false);

    const projP = client.createProject({ name: 'pr' });
    await flush(300);
    const project = await projP;

    const svcP = client.createService(project.id, {
      name: 'default',
      repo_url: 'https://gitea.local/o/r.git',
      git_mode: 'draft_pr',
    });
    await flush(300);
    await svcP;

    const p2 = client.getSystem();
    await flush(200);
    expect((await p2).provider.gitea_enabled).toBe(true);
  });
});

// PR review flow (blueprint §5): getPR reports a live PR + reviews; requestReview
// spawns a kind=review run that produces markdown output.
describe('mockClient — PR review flow', () => {
  it('getPR returns an open PR, head branch and a baseline review', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);
    await flush(8000); // drive to succeeded (pr_url populated)

    const prP = client.getPR(run.id);
    await flush(200);
    const pr = await prP;
    expect(pr.state).toBe('open');
    expect(pr.url).toContain('/pulls/');
    expect(pr.head_branch).toContain(run.id);
    expect(pr.review_runs.length).toBeGreaterThanOrEqual(1);
    expect(pr.review_runs.some((r) => r.review_output.length > 0)).toBe(true);
  });

  it('requestReview creates a review run that produces markdown output', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);
    await flush(8000); // succeeded

    const revP = client.requestReview(run.id);
    await flush(200);
    const review = await revP;
    expect(review.kind).toBe('review');
    expect(review.status).toBe('queued');

    await flush(4000); // drive the review playback to succeeded
    const doneP = client.getRun(review.id);
    await flush(200);
    const done = await doneP;
    expect(done.status).toBe('succeeded');
    expect(done.review_output).toContain('AI review');

    // The new review now appears in the PR's review list.
    const prP = client.getPR(run.id);
    await flush(200);
    const pr = await prP;
    expect(pr.review_runs.some((r) => r.id === review.id)).toBe(true);
  });

  it('requestReview on a non-succeeded run is a 409 conflict', async () => {
    const client = createMockClient();
    const { run } = await makeProjectAndRun(client);
    await flush(1300); // running (non-terminal, no PR yet)

    const p = client.requestReview(run.id).then(
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
});

describe('mockClient — project guardrails (Feature B)', () => {
  it('persists guardrails through updateProject + projectView', async () => {
    const client = createMockClient();
    const cp = client.createProject({ name: 'demo' });
    await flush(500);
    const project = await cp;

    const up = client.updateProject(project.id, {
      max_concurrent_runs: 3,
      run_timeout_secs: 600,
      injected_env: { COMPANY_TOKEN: 'abc' },
    });
    await flush(500);
    const updated = await up;
    expect(updated.max_concurrent_runs).toBe(3);
    expect(updated.run_timeout_secs).toBe(600);
    expect(updated.injected_env).toEqual({ COMPANY_TOKEN: 'abc' });

    // Clearing with null/≤0 drops back to inherit (omitted).
    const up2 = client.updateProject(project.id, { max_concurrent_runs: null });
    await flush(500);
    const cleared = await up2;
    expect(cleared.max_concurrent_runs).toBeUndefined();
  });

  it('rejects a reserved injected_env key with a typed 400', async () => {
    const client = createMockClient();
    const cp = client.createProject({ name: 'demo' });
    await flush(500);
    const project = await cp;

    const p = client
      .updateProject(project.id, { injected_env: { RUN_TOKEN: 'evil' } })
      .then(() => ({ ok: true as const }), (e) => ({ ok: false as const, err: e }));
    await flush(500);
    const res = await p;
    expect(res.ok).toBe(false);
    if (!res.ok) {
      expect(res.err).toBeInstanceOf(ApiError);
      expect((res.err as ApiError).status).toBe(400);
      expect(((res.err as ApiError).body as { error?: { code?: string } })?.error?.code).toBe('reserved_env_key');
    }
  });

  it('rejects a deprecated provider_allowlist PATCH with a typed 400 (D20/F5)', async () => {
    const client = createMockClient();
    const cp = client.createProject({ name: 'demo' });
    await flush(500);
    const project = await cp;

    const p = client
      .updateProject(project.id, { provider_allowlist: ['github'] } as never)
      .then(() => ({ ok: true as const }), (e) => ({ ok: false as const, err: e }));
    await flush(500);
    const res = await p;
    expect(res.ok).toBe(false);
    if (!res.ok) {
      expect((res.err as ApiError).status).toBe(400);
      expect(((res.err as ApiError).body as { error?: { code?: string } })?.error?.code).toBe('deprecated_key');
    }
  });
});

describe('mockClient — cluster kanban config (D27)', () => {
  it('roundtrips GET/PUT/DELETE and reflects the effective config into getSystem', async () => {
    const client = createMockClient();

    // Initially off: no DB row and no env fallback in the demo rig ⇒ source none.
    const g0 = client.getKanbanConfig();
    await flush(200);
    const c0 = await g0;
    expect(c0.source).toBe('none');
    expect(c0.effective_enabled).toBe(false);
    expect(c0.base_url).toBe('');

    // getSystem agrees the integration is off.
    const s0 = client.getSystem();
    await flush(200);
    expect((await s0).kanban?.enabled).toBe(false);

    // PUT base_url + token ⇒ source flips to db, effective on.
    const u1 = client.updateKanbanConfig({ base_url: 'http://jtype:13345', token: 'jt-pat' });
    await flush(200);
    const c1 = await u1;
    expect(c1.source).toBe('db');
    expect(c1.base_url).toBe('http://jtype:13345');
    expect(c1.token_set).toBe(true);
    expect(c1.effective_enabled).toBe(true);
    // The token is never echoed back in the view.
    expect(JSON.stringify(c1)).not.toContain('jt-pat');

    // getSystem now reflects the mutable config + its source (runtime activation).
    const s1 = client.getSystem();
    await flush(200);
    const sys1 = await s1;
    expect(sys1.kanban?.enabled).toBe(true);
    expect(sys1.kanban?.source).toBe('db');
    expect(sys1.kanban?.base_url).toBe('http://jtype:13345');

    // Token OMITTED keeps token_set; base_url still updates.
    const u2 = client.updateKanbanConfig({ base_url: 'http://jtype2:13345' });
    await flush(200);
    const c2 = await u2;
    expect(c2.base_url).toBe('http://jtype2:13345');
    expect(c2.token_set).toBe(true); // unchanged

    // Explicit clear (token:"") drops the fallback token.
    const u3 = client.updateKanbanConfig({ base_url: 'http://jtype2:13345', token: '' });
    await flush(200);
    expect((await u3).token_set).toBe(false);

    // An invalid base_url is a typed 400 and leaves state untouched.
    const bad = client
      .updateKanbanConfig({ base_url: 'not-a-url' })
      .then(() => ({ ok: true as const }), (e) => ({ ok: false as const, err: e }));
    await flush(200);
    const badRes = await bad;
    expect(badRes.ok).toBe(false);
    if (!badRes.ok) {
      expect(badRes.err).toBeInstanceOf(ApiError);
      expect((badRes.err as ApiError).status).toBe(400);
    }

    // DELETE drops the override ⇒ back to source none / off.
    const d = client.deleteKanbanConfig();
    await flush(200);
    const c4 = await d;
    expect(c4.source).toBe('none');
    expect(c4.effective_enabled).toBe(false);
    const s2 = client.getSystem();
    await flush(200);
    expect((await s2).kanban?.enabled).toBe(false);
  });
});

describe('mockClient — kanban "Connect with jtype" device flow (D28)', () => {
  it('cluster: requires a base_url, then roundtrips pending→complete sealing a 90d token', async () => {
    const client = createMockClient();

    // With no DB base_url the flow can't start — fail-visible 409, mirroring the
    // orchestrator's base_url_not_configured (D27 same-source binding).
    const noBase = client
      .startKanbanConnect()
      .then(() => ({ ok: true as const }), (err) => ({ ok: false as const, err }));
    await flush(200);
    const nb = await noBase;
    expect(nb.ok).toBe(false);
    if (!nb.ok) {
      expect(nb.err).toBeInstanceOf(ApiError);
      expect((nb.err as ApiError).status).toBe(409);
    }

    // Save a base URL, then start: a 6-digit user_code + a deep link carrying it,
    // and NO device_code (the mint secret is withheld from the browser).
    const u = client.updateKanbanConfig({ base_url: 'http://jtype:13345' });
    await flush(200);
    await u;
    const s = client.startKanbanConnect();
    await flush(200);
    const start = await s;
    expect(start.user_code).toMatch(/^\d{6}$/);
    expect(start.verification_uri_complete).toContain(start.user_code);
    expect(JSON.stringify(start)).not.toContain('device_code');

    // First poll is pending; the second flips to complete and seals the token.
    const p1 = client.pollKanbanConnect(start.connect_id);
    await flush(200);
    expect((await p1).status).toBe('pending');
    const p2 = client.pollKanbanConnect(start.connect_id);
    await flush(200);
    const done = await p2;
    expect(done.status).toBe('complete');
    expect(done.token_set).toBe(true);
    expect(done.token_expires_at).toBeTruthy();
    // ~90 days out (MCP_TOKEN_TTL_SECS), never a plaintext token in the body.
    const days = Math.round((new Date(done.token_expires_at!).getTime() - Date.now()) / 86_400_000);
    expect(days).toBe(90);

    // getKanbanConfig now reflects the sealed fallback token + its expiry.
    const g = client.getKanbanConfig();
    await flush(200);
    const cfg = await g;
    expect(cfg.token_set).toBe(true);
    expect(cfg.token_expires_at).toBe(done.token_expires_at);

    // An unknown connect_id is 404 connect_expired (dropped/expired flow).
    const unk = client
      .pollKanbanConnect('kc_nope')
      .then(() => ({ ok: true as const }), (err) => ({ ok: false as const, err }));
    await flush(200);
    const u2 = await unk;
    expect(u2.ok).toBe(false);
    if (!u2.ok) expect((u2.err as ApiError).status).toBe(404);
  });

  it('per-link: connect seals a token and flips credential_status to per_link', async () => {
    const client = createMockClient();
    // Cluster integration must be effective for a per-link connect.
    const u = client.updateKanbanConfig({ base_url: 'http://jtype:13345' });
    await flush(200);
    await u;

    const cp = client.createProject({ name: 'demo' });
    await flush(500);
    const project = await cp;
    const cs = client.createService(project.id, {
      name: 'default',
      repo_url: 'https://gitea.local/acme/demo.git',
      default_branch: 'main',
    });
    await flush(500);
    const svc = await cs;

    // Create the link with a BLANK token (create-then-connect) ⇒ missing.
    const cl = client.createProjectKanbanLink(project.id, {
      workspace_id: 'ws',
      board_ref: 'b',
      service_id: svc.id,
      trigger_column: 'ai',
    });
    await flush(200);
    const link = await cl;
    expect(link.credential_status).toBe('missing');

    // Connect and poll to completion.
    const s = client.startLinkConnect(project.id, link.id);
    await flush(200);
    const start = await s;
    expect(start.user_code).toMatch(/^\d{6}$/);
    const first = client.pollLinkConnect(project.id, link.id, start.connect_id);
    await flush(200);
    expect((await first).status).toBe('pending');
    const second = client.pollLinkConnect(project.id, link.id, start.connect_id);
    await flush(200);
    const done = await second;
    expect(done.status).toBe('complete');

    // The link now owns its token, with the device-flow expiry.
    const ll = client.listProjectKanbanLinks(project.id);
    await flush(200);
    const links = await ll;
    expect(links[0]!.credential_status).toBe('per_link');
    expect(links[0]!.token_set).toBe(true);
    expect(links[0]!.token_expires_at).toBe(done.token_expires_at);

    // A link that isn't this project's ⇒ 404.
    const foreign = client
      .startLinkConnect('other-project', link.id)
      .then(() => ({ ok: true as const }), (err) => ({ ok: false as const, err }));
    await flush(200);
    const f = await foreign;
    expect(f.ok).toBe(false);
    if (!f.ok) expect((f.err as ApiError).status).toBe(404);
  });
});

describe('mockClient — kanban discovery pickers (D29)', () => {
  it('lists workspaces + boards-with-columns once the integration is effective', async () => {
    const client = createMockClient();
    const cp = client.createProject({ name: 'demo' });
    await flush(500);
    const project = await cp;

    // Integration off ⇒ discovery is fail-visible (typed 409), never an empty 200.
    const off = client
      .listJtypeWorkspaces(project.id)
      .then(() => ({ ok: true as const }), (err) => ({ ok: false as const, err }));
    await flush(200);
    const offR = await off;
    expect(offR.ok).toBe(false);
    if (!offR.ok) {
      expect((offR.err as ApiError).status).toBe(409);
      expect(apiErrorCode(offR.err)).toBe('kanban_not_configured');
    }

    // Turn the cluster integration on.
    const u = client.updateKanbanConfig({ base_url: 'http://jtype:13345' });
    await flush(200);
    await u;

    const ws = client.listJtypeWorkspaces(project.id);
    await flush(200);
    const workspaces = await ws;
    expect(workspaces.length).toBeGreaterThan(0);
    expect(workspaces[0]).toHaveProperty('id');
    expect(workspaces[0]).toHaveProperty('name');

    const bs = client.listJtypeBoards(project.id, workspaces[0]!.id);
    await flush(200);
    const boards = await bs;
    expect(boards.length).toBeGreaterThan(0);
    const board = boards[0]!;
    // A board carries its config id, submittable ref, title, and columns.
    expect(board.id).toMatch(/^b_/);
    expect(board.ref).toMatch(/\.board$/);
    expect(board.columns.length).toBeGreaterThan(0);
    expect(board.columns[0]).toHaveProperty('key');
    expect(board.columns[0]).toHaveProperty('name');

    // No token ever crosses the wire in a discovery response.
    expect(JSON.stringify(workspaces)).not.toContain('token');
    expect(JSON.stringify(boards)).not.toContain('token');
  });

  it('an unknown workspace id is a typed workspace_not_found (not a silent empty list)', async () => {
    const client = createMockClient();
    const cp = client.createProject({ name: 'demo' });
    await flush(500);
    const project = await cp;
    const u = client.updateKanbanConfig({ base_url: 'http://jtype:13345' });
    await flush(200);
    await u;

    const r = client
      .listJtypeBoards(project.id, 'ws_does_not_exist')
      .then(() => ({ ok: true as const }), (err) => ({ ok: false as const, err }));
    await flush(200);
    const res = await r;
    expect(res.ok).toBe(false);
    if (!res.ok) {
      expect((res.err as ApiError).status).toBe(400);
      expect(apiErrorCode(res.err)).toBe('workspace_not_found');
    }
  });

  it('a soft-created (tokenless) link is board_status "unvalidated"; a tokened link is "ok"', async () => {
    const client = createMockClient();
    const cp = client.createProject({ name: 'demo' });
    await flush(500);
    const project = await cp;
    const cs = client.createService(project.id, {
      name: 'default',
      repo_url: 'https://gitea.local/acme/demo.git',
      default_branch: 'main',
    });
    await flush(500);
    const svc = await cs;

    const soft = client.createProjectKanbanLink(project.id, {
      workspace_id: 'ws_team',
      board_ref: 'jtype.board',
      service_id: svc.id,
      trigger_column: 'ai',
    });
    await flush(200);
    expect((await soft).board_status).toBe('unvalidated');

    const hard = client.createProjectKanbanLink(project.id, {
      workspace_id: 'ws_team',
      board_ref: 'Jcode.board',
      service_id: svc.id,
      trigger_column: 'agent',
      token: 'jtype-pat',
    });
    await flush(200);
    const link = await hard;
    expect(link.board_status).toBe('ok');
    // The discoverable board's title is captured for a friendly row label.
    expect(link.board_title).toBe('Jcode');
  });
});

describe('mockClient integrations (D19 / F5)', () => {
  it('creates, lists, rotates and deletes integrations; token is never echoed', async () => {
    const client = createMockClient();
    const cp = client.createProject({ name: 'demo' });
    await flush(500);
    const project = await cp;

    const ci = client.createIntegration(project.id, {
      provider: 'gitea',
      host: 'gitea.example.com',
      token: 'secret-pat',
    });
    await flush(500);
    const integ = await ci;
    expect(integ.token_set).toBe(true);
    expect(integ.bot_username).toBe('gitea-bot');
    expect(JSON.stringify(integ)).not.toContain('secret-pat');

    const li = client.listIntegrations(project.id);
    await flush(500);
    expect((await li).length).toBe(1);

    // A member can build a service off the integration (integration_id set).
    const cs = client.createService(project.id, {
      name: 'widget',
      owner_name: 'acme/widget',
      integration_id: integ.id,
      git_mode: 'draft_pr',
    });
    await flush(500);
    const svc = await cs;
    expect(svc.integration_id).toBe(integ.id);
    expect(svc.provider).toBe('gitea');

    // Delete unbinds the service.
    const di = client.deleteIntegration(integ.id);
    await flush(500);
    await di;
    const ls = client.listServices(project.id);
    await flush(500);
    expect((await ls).find((s) => s.id === svc.id)?.integration_id ?? null).toBeNull();
  });
});
