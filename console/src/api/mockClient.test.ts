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
