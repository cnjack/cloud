/*
 * mockClient.ts — in-memory ApiClient with fake SSE run playback.
 *
 * Purpose (per brief): make the whole UI demo-able before the orchestrator is
 * up (VITE_DEMO=1) AND e2e-testable without a cluster. It implements the exact
 * ApiClient interface, drives a realistic run lifecycle
 * (queued → scheduling → running → succeeded|failed) and emits sequenced events
 * over time that streamRun() replays-then-follows just like the real stream.
 *
 * Determinism: a run whose prompt contains "fail" or points at a project with a
 * bad repo URL fails at the clone stage — this makes J2 (failure + retry)
 * demoable. Everything else succeeds and produces a README diff (J1).
 */
import type { ApiClient, StreamCallbacks, StreamHandle } from './client';
import { ApiError } from './client';
import type {
  CreateProjectInput,
  CreateRunInput,
  FailureReason,
  Project,
  Run,
  RunArtifact,
  RunEvent,
  RunEventType,
  RunStatus,
} from './types';

let idCounter = 1;
function genId(prefix: string): string {
  const n = (idCounter++).toString(36).padStart(4, '0');
  const rand = Math.random().toString(36).slice(2, 6);
  return `${prefix}_${n}${rand}`;
}

function nowISO(offsetMs = 0): string {
  return new Date(Date.now() + offsetMs).toISOString();
}

interface StoredRun extends Run {
  _events: RunEvent[];
  _diff?: string;
  _timers: ReturnType<typeof setTimeout>[];
  _subs: Set<(ev: RunEvent) => void>;
  _statusSubs: Set<(run: Run) => void>;
}

const DEMO_SPEED = Number(import.meta.env?.VITE_DEMO_SPEED ?? 1) || 1;
function ms(base: number): number {
  return Math.max(1, Math.round(base / DEMO_SPEED));
}

const SAMPLE_DIFF = `diff --git a/README.md b/README.md
index 3b18e51..9daeafb 100644
--- a/README.md
+++ b/README.md
@@ -1,3 +1,4 @@
 # demo

 A tiny sample repository used by jcode Cloud Agent.
+Hello
`;

export function createMockClient(): ApiClient {
  const projects = new Map<string, Project>();
  const runs = new Map<string, StoredRun>();

  // Seed one project so demo mode isn't a cold empty state after first click.
  // J1's empty-state assertion still holds because seeding is opt-in via env.
  if (import.meta.env?.VITE_DEMO_SEED === '1') {
    const p: Project = {
      id: genId('proj'),
      name: 'demo',
      repo_url: 'https://gitea.local/acme/demo.git',
      default_branch: 'main',
      created_at: nowISO(-3600_000),
    };
    projects.set(p.id, p);
  }

  function emit(run: StoredRun, type: RunEventType, payload: RunEvent['payload']) {
    const ev: RunEvent = {
      seq: run._events.length + 1,
      ts: nowISO(),
      type,
      payload,
    };
    run._events.push(ev);
    for (const fn of run._subs) fn(ev);
  }

  function setStatus(run: StoredRun, status: RunStatus) {
    run.status = status;
    if (status === 'running' && !run.started_at) run.started_at = nowISO();
    if (
      (status === 'succeeded' || status === 'failed' || status === 'canceled') &&
      !run.finished_at
    ) {
      run.finished_at = nowISO();
    }
    emit(run, 'run.status', { status });
    for (const fn of run._statusSubs) fn(publicRun(run));
  }

  function fail(run: StoredRun, reason: FailureReason, message: string) {
    run.failure_reason = reason;
    run.failure_message = message;
    run.error = message;
    setStatus(run, 'failed');
    emit(run, 'run.failure', { reason, message });
  }

  function schedule(run: StoredRun, delay: number, fn: () => void) {
    run._timers.push(setTimeout(fn, ms(delay)));
  }

  function startPlayback(run: StoredRun) {
    const project = projects.get(run.project_id);
    const willFail =
      /\bfail\b/i.test(run.prompt) ||
      (project ? /(bad|invalid|nonexistent|does-not-exist)/i.test(project.repo_url) : false);

    schedule(run, 400, () => setStatus(run, 'scheduling'));
    schedule(run, 1200, () => {
      setStatus(run, 'running');
      emit(run, 'agent.text', {
        text: `Cloning ${project?.repo_url ?? 'repository'} (branch ${
          project?.default_branch ?? 'main'
        })…`,
      });
    });

    if (willFail) {
      schedule(run, 2600, () => {
        emit(run, 'agent.tool_result', {
          tool: 'git.clone',
          call_id: 'clone',
          ok: false,
          exit_code: 128,
          output:
            'fatal: repository not found\nremote: The requested repository does not exist.',
        });
      });
      schedule(run, 3200, () =>
        fail(
          run,
          'clone_failed',
          `Could not clone ${project?.repo_url ?? 'the repository'}: repository not found. Check the repo URL and clone credentials.`,
        ),
      );
      return;
    }

    // Happy path: a few tool calls, some prose, then a diff artifact + success.
    schedule(run, 2400, () => {
      emit(run, 'agent.text', {
        text: 'Repository cloned. Reading the working tree to plan the change.',
      });
      emit(run, 'agent.tool_call', {
        tool: 'read_file',
        call_id: 'c1',
        args: { path: 'README.md' },
      });
    });
    schedule(run, 3300, () => {
      emit(run, 'agent.tool_result', {
        tool: 'read_file',
        call_id: 'c1',
        ok: true,
        output: '# demo\n\nA tiny sample repository used by jcode Cloud Agent.\n',
      });
      emit(run, 'agent.text', {
        text: 'I will append a line `Hello` to the end of README.md.',
      });
    });
    schedule(run, 4200, () => {
      emit(run, 'agent.tool_call', {
        tool: 'edit_file',
        call_id: 'c2',
        args: {
          path: 'README.md',
          instruction: 'Append "Hello" as a new final line.',
        },
      });
    });
    schedule(run, 5100, () => {
      emit(run, 'agent.tool_result', {
        tool: 'edit_file',
        call_id: 'c2',
        ok: true,
        output: 'Applied edit to README.md (+1 line).',
      });
      emit(run, 'agent.text', {
        text: 'Change applied. Producing the unified diff for review.',
      });
    });
    // ST-1 demo: showcase the Gitea draft-PR closed loop. The runner pushes the
    // agent/run-<id> branch (run.git), then the diff artifact lands, then the run
    // succeeds — and the draft PR link is populated on the SAME succeeded frame so
    // the "Draft PR #N" chip is present the moment the header goes terminal. (The
    // real orchestrator opens the PR just after success; the console likewise
    // picks up pr_url via its terminal refetch — this keeps the demo showcase
    // deterministic without depending on a post-terminal stream frame.)
    const branch = `agent/run-${run.id}`;
    schedule(run, 5800, () => {
      emit(run, 'run.git', { branch, commit_sha: 'a1b2c3d4' });
    });
    schedule(run, 6000, () => {
      run._diff = SAMPLE_DIFF;
      emit(run, 'run.artifact', { kind: 'diff' });
      // Populate the draft PR BEFORE the terminal transition so the run object the
      // terminal refetch reads (mock getRun) already carries pr_url — the chip is
      // present the moment the header goes terminal.
      run.pr_url = 'https://gitea.local/jcloud/seed/pulls/42';
      run.pr_number = 42;
      setStatus(run, 'succeeded');
    });
  }

  function publicRun(run: StoredRun): Run {
    // Strip the private playback fields.
    const {
      _events,
      _diff,
      _timers,
      _subs,
      _statusSubs,
      ...pub
    } = run;
    void _events;
    void _diff;
    void _timers;
    void _subs;
    void _statusSubs;
    return { ...pub };
  }

  async function delay<T>(value: T): Promise<T> {
    await new Promise((r) => setTimeout(r, ms(120)));
    return value;
  }

  function makeRun(
    projectId: string,
    prompt: string,
    retriedFrom?: string,
    attempt = 1,
  ): StoredRun {
    const run: StoredRun = {
      id: genId('run'),
      project_id: projectId,
      prompt,
      status: 'queued',
      attempt,
      retried_from: retriedFrom ?? null,
      created_at: nowISO(),
      started_at: null,
      finished_at: null,
      pr_url: null,
      pr_number: null,
      _events: [],
      _timers: [],
      _subs: new Set(),
      _statusSubs: new Set(),
    };
    runs.set(run.id, run);
    startPlayback(run);
    return run;
  }

  return {
    async listProjects() {
      return delay(
        [...projects.values()].sort((a, b) =>
          b.created_at.localeCompare(a.created_at),
        ),
      );
    },

    async createProject(input: CreateProjectInput) {
      const p: Project = {
        id: genId('proj'),
        name: input.name,
        repo_url: input.repo_url,
        default_branch: input.default_branch || 'main',
        created_at: nowISO(),
      };
      projects.set(p.id, p);
      return delay(p);
    },

    async getProject(id: string) {
      const p = projects.get(id);
      if (!p) throw new ApiError(404, 'project not found');
      return delay(p);
    },

    async listRuns(projectId: string) {
      return delay(
        [...runs.values()]
          .filter((r) => r.project_id === projectId)
          .sort((a, b) => b.created_at.localeCompare(a.created_at))
          .map(publicRun),
      );
    },

    async createRun(projectId: string, input: CreateRunInput) {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      return delay(publicRun(makeRun(projectId, input.prompt)));
    },

    async getRun(runId: string) {
      const r = runs.get(runId);
      if (!r) throw new ApiError(404, 'run not found');
      return delay(publicRun(r));
    },

    async cancelRun(runId: string) {
      const r = runs.get(runId);
      if (!r) throw new ApiError(404, 'run not found');
      // 11-api.md §2.2: cancel on an already-terminal run is a 409 conflict.
      // Match the HTTP client so demo/e2e exercise the same conflict path.
      if (['succeeded', 'failed', 'canceled'].includes(r.status)) {
        throw new ApiError(409, 'run already finished', {
          error: { code: 'conflict', message: 'run already finished' },
        });
      }
      for (const t of r._timers) clearTimeout(t);
      r._timers = [];
      setStatus(r, 'canceled');
      return delay(publicRun(r));
    },

    async retryRun(runId: string) {
      const orig = runs.get(runId);
      if (!orig) throw new ApiError(404, 'run not found');
      // 11-api.md §2.2: only terminal runs may be retried; retry on a
      // non-terminal run is a 409 conflict. The new run's attempt = orig + 1.
      if (!['succeeded', 'failed', 'canceled'].includes(orig.status)) {
        throw new ApiError(409, 'run not finished', {
          error: { code: 'conflict', message: 'run not finished' },
        });
      }
      return delay(
        publicRun(
          makeRun(orig.project_id, orig.prompt, orig.id, (orig.attempt ?? 1) + 1),
        ),
      );
    },

    async listEvents(runId: string, afterSeq = 0) {
      const r = runs.get(runId);
      if (!r) throw new ApiError(404, 'run not found');
      return delay(r._events.filter((e) => e.seq > afterSeq));
    },

    streamRun(runId: string, afterSeq: number, cb: StreamCallbacks): StreamHandle {
      const r = runs.get(runId);
      if (!r) {
        cb.onError?.(new ApiError(404, 'run not found'));
        return { close: () => {} };
      }
      let closed = false;
      const push = (ev: RunEvent) => {
        if (!closed) cb.onFrame({ event: ev.type, data: ev });
      };

      // Replay backlog (seq > afterSeq) on next tick, then attach live.
      const replayTimer = setTimeout(() => {
        cb.onOpen?.();
        for (const ev of r._events) {
          if (ev.seq > afterSeq) push(ev);
        }
        r._subs.add(push);
      }, 0);

      return {
        close: () => {
          closed = true;
          clearTimeout(replayTimer);
          r._subs.delete(push);
        },
      };
    },

    async getDiff(runId: string) {
      const r = runs.get(runId);
      if (!r) throw new ApiError(404, 'run not found');
      if (!r._diff) throw new ApiError(404, 'diff artifact not ready');
      const artifact: RunArtifact = {
        run_id: runId,
        kind: 'diff',
        content: r._diff,
        created_at: r.finished_at ?? nowISO(),
      };
      return delay(artifact);
    },

    diffDownloadUrl(runId: string) {
      const r = runs.get(runId);
      const content = r?._diff ?? '';
      // A data: URL keeps download working with no server in demo mode.
      return `data:text/plain;charset=utf-8,${encodeURIComponent(content)}`;
    },
  };
}
