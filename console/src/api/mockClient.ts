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
  AddMemberInput,
  CreateKanbanLinkInput,
  CreateProjectInput,
  CreateRunInput,
  CreateServiceInput,
  FailureReason,
  KanbanLink,
  Me,
  Member,
  MemberRole,
  ModelConfigInfo,
  PrInfo,
  Project,
  PutModelConfigInput,
  ReviewRunSummary,
  Run,
  RunArtifact,
  RunEvent,
  RunEventType,
  RunStatus,
  Service,
  SystemInfo,
  UpdateProjectInput,
  UserSearchResult,
} from './types';
import { providerForRepoUrl } from '../lib/repo';
import { isReservedEnvKey, isValidEnvKey } from '../lib/env';
import { ALLOWLIST_PROVIDERS } from '../lib/providers';

/**
 * providerAllowed mirrors the orchestrator guardrail: an empty/absent allowlist
 * imposes no restriction; a raw repo (no provider) is addressed by "raw".
 */
function providerAllowed(allowlist: string[] | undefined, provider?: string): boolean {
  if (!allowlist || allowlist.length === 0) return true;
  const p = (provider ?? '').trim() || 'raw';
  return allowlist.some((a) => a.trim().toLowerCase() === p);
}

let idCounter = 1;
function genId(prefix: string): string {
  const n = (idCounter++).toString(36).padStart(4, '0');
  const rand = Math.random().toString(36).slice(2, 6);
  return `${prefix}_${n}${rand}`;
}

function nowISO(offsetMs = 0): string {
  return new Date(Date.now() + offsetMs).toISOString();
}

/**
 * Build a 400 ApiError with the same nested envelope shape the HTTP client
 * parses (11-api.md §0), so validation errors read identically in demo/e2e.
 */
function badRequest(message: string): ApiError {
  return new ApiError(400, message, {
    error: { code: 'bad_request', message },
  });
}

interface StoredRun extends Run {
  _events: RunEvent[];
  _diff?: string;
  /** For a kind=review run: the agent run id whose PR it reviews. */
  _reviewFor?: string;
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

/** A finished AI review's markdown output (demo mode). Exercises the markdown
 *  renderer: headings, a list, bold, inline code and a fenced code block. */
const SAMPLE_REVIEW_MD = `## AI review

Overall this change is **small and safe**. A couple of notes:

- The new \`Hello\` line is appended cleanly to \`README.md\`.
- Consider whether the file should end with a trailing newline.

\`\`\`diff
+Hello
\`\`\`

**Verdict:** looks good to merge.`;

/**
 * Demo identity (VITE_DEMO): a signed-in cluster-admin user so the identity chip,
 * members picker and link affordances all have realistic data without a backend.
 */
const DEMO_ME: Me = {
  user: {
    id: 'u_ada',
    display_name: 'Ada Lovelace',
    avatar_url: '',
    is_cluster_admin: true,
  },
  is_service: false,
  identities: [{ provider: 'gitea', username: 'ada' }],
};

const DEMO_USERS: UserSearchResult[] = [
  { id: 'u_ada', display_name: 'Ada Lovelace', is_cluster_admin: true },
  { id: 'u_grace', display_name: 'Grace Hopper', is_cluster_admin: false },
  { id: 'u_alan', display_name: 'Alan Turing', is_cluster_admin: false },
  { id: 'u_katherine', display_name: 'Katherine Johnson', is_cluster_admin: false },
];

/** "owner/name" from a provider-shaped http(s) URL, or "" otherwise. */
function ownerName(raw: string): string {
  try {
    const u = new URL(raw.trim());
    const parts = u.pathname
      .replace(/\.git$/, '')
      .split('/')
      .filter(Boolean);
    return parts.length >= 2 ? `${parts[0]}/${parts[1]}` : '';
  } catch {
    return '';
  }
}

export function createMockClient(): ApiClient {
  const projects = new Map<string, Project>();
  const runs = new Map<string, StoredRun>();
  // Services + members keyed by project id (blueprint §1/§2). A project starts
  // with a single 'default' service — the "one repo = one project" simple UX.
  const services = new Map<string, Service[]>();
  const members = new Map<string, Member[]>();
  // Feature E: kanban links (board→service bindings), keyed by link id.
  const kanbanLinks = new Map<string, KanbanLink>();

  // Feature A: the cluster model config. Demo starts CONFIGURED (source=env, as
  // the local rig would) so the composer is enabled; the Cluster page's admin
  // form mutates it via set/clear.
  let modelConfig: ModelConfigInfo = {
    configured: true,
    source: 'env',
    base_url: 'http://mockllm.jcloud.svc.cluster.local:8081/v1',
    model_name: 'mock/mock-model',
    api_key_set: true,
  };

  const asMember = (u: UserSearchResult, role: MemberRole): Member => ({
    user_id: u.id,
    role,
    display_name: u.display_name,
    avatar_url: u.avatar_url,
    username: u.id === 'u_ada' ? 'ada' : undefined,
    is_cluster_admin: u.is_cluster_admin,
  });

  /** Attach the services array + the demo principal's role onto a project view. */
  function projectView(p: Project): Project {
    return {
      ...p,
      role: 'owner',
      owner_user_id: DEMO_ME.user.id,
      services: services.get(p.id) ?? [],
    };
  }

  /** Register a project (a pure container) with its services + owner membership. */
  function registerProject(p: Project, svcs: Service[] = []): void {
    projects.set(p.id, p);
    services.set(p.id, svcs);
    members.set(p.id, [asMember(DEMO_USERS[0]!, 'owner')]);
  }

  /** Build a seeded service (demo fixtures only). */
  function seedService(
    projectId: string,
    ownerNamePath: string,
    gitMode: Service['git_mode'],
    createdAt: string,
  ): Service {
    return {
      id: genId('svc'),
      project_id: projectId,
      name: 'default',
      repo_kind: 'provider',
      provider: 'gitea',
      repo_owner_name: ownerNamePath,
      default_branch: 'main',
      git_mode: gitMode,
      created_at: createdAt,
    };
  }

  // Seed projects so demo mode isn't a cold empty state after first click.
  // J1's empty-state assertion still holds because seeding is opt-in via env.
  // Two projects showcase both git modes (F5): a readonly diff-only project and
  // a draft_pr project whose runs open a Gitea draft PR.
  if (import.meta.env?.VITE_DEMO_SEED === '1') {
    const readonly: Project = {
      id: genId('proj'),
      name: 'demo',
      created_at: nowISO(-3600_000),
    };
    registerProject(readonly, [seedService(readonly.id, 'acme/demo', 'readonly', readonly.created_at)]);

    const draftPr: Project = {
      id: genId('proj'),
      name: 'seed (draft PR)',
      created_at: nowISO(-1800_000),
    };
    registerProject(draftPr, [seedService(draftPr.id, 'jcloud/seed', 'draft_pr', draftPr.created_at)]);
    // Seed a second member so the members tab has something to show.
    members.get(draftPr.id)!.push(asMember(DEMO_USERS[1]!, 'viewer'));
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
    // Repo identity lives on the run's service now (a project is a pure
    // container); makeRun sets service_id before playback starts.
    const svc = (services.get(run.project_id) ?? []).find((s) => s.id === run.service_id);
    const repoLabel = svc?.repo_owner_name ?? svc?.raw_repo_url ?? 'repository';
    const willFail =
      /\bfail\b/i.test(run.prompt) ||
      /(bad|invalid|nonexistent|does-not-exist)/i.test(repoLabel);

    schedule(run, 400, () => setStatus(run, 'scheduling'));
    schedule(run, 1200, () => {
      setStatus(run, 'running');
      emit(run, 'agent.text', {
        // Trailing space: this chunk and the next agent.text emit are
        // consecutive with nothing interleaved, so the Timeline (runview)
        // merges them into one prose block — chunk boundaries must carry
        // their own whitespace, same as real ACP token/sentence chunks do.
        text: `Cloning ${repoLabel} (branch ${svc?.default_branch ?? 'main'})… `,
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
          `Could not clone ${repoLabel}: repository not found. Check the repo URL and clone credentials.`,
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
      _reviewFor,
      _timers,
      _subs,
      _statusSubs,
      ...pub
    } = run;
    void _events;
    void _diff;
    void _reviewFor;
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
    serviceId: string | undefined,
    prompt: string,
    retriedFrom?: string,
    attempt = 1,
  ): StoredRun {
    const run: StoredRun = {
      id: genId('run'),
      project_id: projectId,
      service_id: serviceId,
      kind: 'agent',
      prompt,
      status: 'queued',
      attempt,
      retried_from: retriedFrom ?? null,
      created_at: nowISO(),
      started_at: null,
      finished_at: null,
      pr_url: null,
      pr_number: null,
      origin: 'api',
      _events: [],
      _timers: [],
      _subs: new Set(),
      _statusSubs: new Set(),
    };
    runs.set(run.id, run);
    startPlayback(run);
    return run;
  }

  /** A review run's playback: queued → running (a little prose) → succeeded with
   *  review_output set, mirroring the runner posting REVIEW.md then the run
   *  succeeding (blueprint §3/§5). */
  function startReviewPlayback(run: StoredRun) {
    schedule(run, 300, () => setStatus(run, 'scheduling'));
    schedule(run, 900, () => {
      setStatus(run, 'running');
      emit(run, 'agent.text', { text: 'Reading the pull request diff…' });
    });
    schedule(run, 2000, () => {
      emit(run, 'agent.text', { text: 'Reviewing the change and drafting comments.' });
    });
    schedule(run, 3200, () => {
      run.review_output = SAMPLE_REVIEW_MD;
      emit(run, 'run.artifact', { kind: 'review' });
      setStatus(run, 'succeeded');
    });
  }

  /** Create a review run against a succeeded agent run's PR. */
  function makeReviewRun(src: StoredRun): StoredRun {
    const run: StoredRun = {
      id: genId('run'),
      project_id: src.project_id,
      service_id: src.service_id,
      kind: 'review',
      prompt: `AI review of PR ${src.pr_url}`,
      status: 'queued',
      attempt: 1,
      retried_from: null,
      created_at: nowISO(),
      started_at: null,
      finished_at: null,
      pr_url: null,
      pr_number: null,
      origin: 'api',
      review_output: '',
      _reviewFor: src.id,
      _events: [],
      _timers: [],
      _subs: new Set(),
      _statusSubs: new Set(),
    };
    runs.set(run.id, run);
    startReviewPlayback(run);
    return run;
  }

  /** Map a stored review run to its PR-tab summary. */
  function reviewSummary(run: StoredRun): ReviewRunSummary {
    return {
      id: run.id,
      status: run.status,
      review_output: run.review_output ?? '',
      review_posted_at: run.status === 'succeeded' ? run.finished_at : null,
      created_at: run.created_at,
      triggered_by_display_name: DEMO_ME.user.display_name,
    };
  }

  return {
    async getMe() {
      return delay(DEMO_ME);
    },

    async listProjects() {
      return delay(
        [...projects.values()]
          .sort((a, b) => b.created_at.localeCompare(a.created_at))
          .map(projectView),
      );
    },

    async createProject(input: CreateProjectInput) {
      // Mirror the orchestrator's create validation (handleCreateProject): a
      // project is a pure container — name is the only field; repos are attached
      // afterwards via createService.
      const name = input.name?.trim() ?? '';
      if (!name) {
        throw badRequest('name is required');
      }
      const p: Project = {
        id: genId('proj'),
        name,
        created_at: nowISO(),
      };
      registerProject(p);
      return delay(projectView(p));
    },

    async getProject(id: string) {
      const p = projects.get(id);
      if (!p) throw new ApiError(404, 'project not found');
      return delay(projectView(p));
    },

    async updateProject(id: string, input: UpdateProjectInput) {
      const existing = projects.get(id);
      if (!existing) throw new ApiError(404, 'project not found');
      // Mirror handleUpdateProject's presence semantics: an omitted field is left
      // unchanged; a numeric guardrail sent as null clears it to "inherit".
      const next: Project = { ...existing };
      if (input.name?.trim()) next.name = input.name.trim();
      if ('max_concurrent_runs' in input) {
        const n = input.max_concurrent_runs;
        next.max_concurrent_runs = n != null && n > 0 ? n : undefined;
      }
      if ('run_timeout_secs' in input) {
        const n = input.run_timeout_secs;
        next.run_timeout_secs = n != null && n > 0 ? n : undefined;
      }
      if ('provider_allowlist' in input) {
        const list = (input.provider_allowlist ?? [])
          .map((p) => p.trim().toLowerCase())
          .filter(Boolean);
        for (const p of list) {
          if (!(ALLOWLIST_PROVIDERS as readonly string[]).includes(p)) {
            throw badRequest(
              `provider_allowlist entry '${p}' is not a known provider (gitea, github, gitlab, raw)`,
            );
          }
        }
        next.provider_allowlist = list.length ? [...new Set(list)] : undefined;
      }
      if ('injected_env' in input) {
        const env = input.injected_env ?? {};
        for (const key of Object.keys(env)) {
          if (!isValidEnvKey(key)) {
            throw badRequest(`injected_env key "${key}" is not a valid environment variable name`);
          }
          if (isReservedEnvKey(key)) {
            // Typed 400 the modal surfaces verbatim (fail-visible parity).
            throw new ApiError(
              400,
              `injected_env key "${key}" is reserved by the orchestrator and cannot be set`,
              {
                error: {
                  code: 'reserved_env_key',
                  message: `injected_env key "${key}" is reserved by the orchestrator and cannot be set`,
                },
              },
            );
          }
        }
        next.injected_env = Object.keys(env).length ? { ...env } : undefined;
      }
      projects.set(id, next);
      return delay(projectView(next));
    },

    async deleteProject(id: string) {
      if (!projects.has(id)) throw new ApiError(404, 'project not found');
      projects.delete(id);
      services.delete(id);
      members.delete(id);
      // Cascade: drop this project's runs (matches the orchestrator's cascade).
      for (const [rid, r] of runs) {
        if (r.project_id === id) {
          for (const t of r._timers) clearTimeout(t);
          runs.delete(rid);
        }
      }
      await new Promise((r) => setTimeout(r, ms(120)));
    },

    async listRuns(projectId: string) {
      return delay(
        [...runs.values()]
          .filter((r) => r.project_id === projectId)
          .sort((a, b) => b.created_at.localeCompare(a.created_at))
          .map(publicRun),
      );
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
          makeRun(orig.project_id, orig.service_id, orig.prompt, orig.id, (orig.attempt ?? 1) + 1),
        ),
      );
    },

    async getPR(runId: string): Promise<PrInfo> {
      const r = runs.get(runId);
      if (!r) throw new ApiError(404, 'run not found');
      // User-requested review runs targeting this PR, newest first.
      const userReviews = [...runs.values()]
        .filter((x) => x.kind === 'review' && x._reviewFor === runId)
        .sort((a, b) => b.created_at.localeCompare(a.created_at))
        .map(reviewSummary);
      // Plus a single pre-existing completed fake review so the tab isn't empty
      // in demo mode (per brief: fake PR state=open + one completed review).
      const baseline: ReviewRunSummary = {
        id: `rev_demo_${runId}`,
        status: 'succeeded',
        review_output: SAMPLE_REVIEW_MD,
        review_posted_at: r.finished_at ?? nowISO(-600_000),
        created_at: nowISO(-600_000),
        triggered_by_display_name: 'Ada Lovelace',
      };
      return delay({
        url: r.pr_url ?? '',
        state: 'open',
        head_branch: `agent/run-${runId}`,
        review_runs: [...userReviews, baseline],
      });
    },

    async requestReview(runId: string) {
      const src = runs.get(runId);
      if (!src) throw new ApiError(404, 'run not found');
      // Mirror the orchestrator preconditions (blueprint §4): a succeeded agent
      // run with a PR. Both surface as 409 conflicts.
      if (src.status !== 'succeeded') {
        throw new ApiError(409, 'only a succeeded run can be reviewed', {
          error: { code: 'conflict', message: 'only a succeeded run can be reviewed' },
        });
      }
      if (!src.pr_url) {
        throw new ApiError(409, 'this run has no pull request to review', {
          error: { code: 'conflict', message: 'this run has no pull request to review' },
        });
      }
      return delay(publicRun(makeReviewRun(src)));
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

    async getSystem(): Promise<SystemInfo> {
      // Derive capacity from the live in-memory runs so the cluster view reflects
      // demo activity (start a run and watch running/scheduling move). Any Gitea
      // draft_pr project flips gitea_enabled so the Provider card is populated.
      let running = 0;
      let queued = 0;
      let scheduling = 0;
      for (const r of runs.values()) {
        if (r.status === 'running') running++;
        else if (r.status === 'queued') queued++;
        else if (r.status === 'scheduling') scheduling++;
      }
      const giteaEnabled = [...services.values()].some((list) =>
        list.some((s) => s.git_mode === 'draft_pr'),
      );
      const info: SystemInfo = {
        version: { version: '1.4.0-demo', commit: 'demo0000' },
        capacity: {
          max_concurrent_runs: 4,
          running,
          queued,
          scheduling,
        },
        guardrails: {
          run_timeout_seconds: 1800,
          job_ttl_seconds: 3600,
        },
        provider: {
          gitea_enabled: giteaEnabled,
          gitea_url: 'http://gitea.jcloud.svc.cluster.local:3000',
        },
        runner: { image: 'ghcr.io/jcloud/runner:demo' },
        namespace: 'jcloud',
        launcher: 'kubernetes',
        auth: {
          providers: ['gitea'],
          users_count: DEMO_USERS.length,
        },
        kanban: {
          enabled: false,
          base_url: '',
          poll_interval: '15s',
        },
      };
      return delay(info);
    },

    /* ---- cluster model config (Feature A) --------------------------------- */
    async getModelConfig(): Promise<ModelConfigInfo> {
      return delay({ ...modelConfig });
    },

    async setModelConfig(input: PutModelConfigInput): Promise<ModelConfigInfo> {
      // Mirror the orchestrator's validation so demo/e2e exercise the same
      // paths. The AUTHORITATIVE rules live in
      // orchestrator/internal/api/system_model.go (validateBaseURL /
      // validateModelName) — reconcile drift there first, then copy here.
      const base = input.base_url?.trim() ?? '';
      if (!/^https?:\/\/.+/i.test(base)) {
        throw badRequest('base_url must be an http(s) URL');
      }
      const model = input.model_name?.trim() ?? '';
      const [provider, ...rest] = model.split('/');
      if (!provider || rest.join('/') === '') {
        throw badRequest("model_name must be in 'provider/model' form");
      }
      modelConfig = {
        configured: true,
        source: 'db',
        base_url: base,
        model_name: model,
        api_key_set: !!input.api_key,
      };
      return delay({ ...modelConfig });
    },

    async clearModelConfig(): Promise<ModelConfigInfo> {
      // KNOWN divergence from the real DELETE: the orchestrator falls back to
      // the MODEL_* env (source 'env') when it is set, while the demo always
      // lands on 'none'. Harmless in practice — the console's Clear button is
      // gated on source==='db', and the demo's initial 'env' state is replaced
      // by 'db' on any save, so this branch only ever follows a db state.
      modelConfig = { configured: false, source: 'none', api_key_set: false };
      return delay({ ...modelConfig });
    },

    /* ---- kanban links (Feature E) ----------------------------------------- */
    async listKanbanLinks(): Promise<KanbanLink[]> {
      return delay([...kanbanLinks.values()]);
    },
    async createKanbanLink(input: CreateKanbanLinkInput): Promise<KanbanLink> {
      const ws = input.workspace_id?.trim();
      const board = input.board_ref?.trim();
      if (!ws || !board || !input.project_id || !input.service_id || !input.trigger_column?.trim()) {
        throw badRequest('workspace_id, board_ref, project_id, service_id and trigger_column are required');
      }
      for (const l of kanbanLinks.values()) {
        if (l.workspace_id === ws && l.board_ref === board) {
          throw new ApiError(409, 'link exists', {
            error: { code: 'already_exists', message: 'a link for this board already exists' },
          });
        }
      }
      const link: KanbanLink = {
        id: 'kl-' + Math.random().toString(36).slice(2, 10),
        workspace_id: ws,
        board_ref: board,
        project_id: input.project_id,
        service_id: input.service_id,
        trigger_column: input.trigger_column.trim(),
        done_column: input.done_column?.trim() || undefined,
        enabled: true,
        created_at: new Date().toISOString(),
      };
      kanbanLinks.set(link.id, link);
      return delay(link);
    },
    async deleteKanbanLink(id: string): Promise<void> {
      if (!kanbanLinks.delete(id)) throw new ApiError(404, 'kanban link not found');
      return delay(undefined);
    },

    /* ---- services (blueprint §4) ------------------------------------------ */
    async listServices(projectId: string) {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      return delay([...(services.get(projectId) ?? [])]);
    },

    async createService(projectId: string, input: CreateServiceInput) {
      const p = projects.get(projectId);
      if (!p) throw new ApiError(404, 'project not found');
      const name = input.name?.trim() || 'default';
      const list = services.get(projectId) ?? [];
      if (list.some((s) => s.name === name)) {
        throw new ApiError(409, `a service named '${name}' already exists`, {
          error: { code: 'conflict', message: `service '${name}' exists` },
        });
      }
      const gitMode = (input.git_mode ?? 'readonly').trim() || 'readonly';
      if (gitMode !== 'readonly' && gitMode !== 'draft_pr') {
        throw badRequest("git_mode must be 'readonly' or 'draft_pr'");
      }
      const repoUrl = input.repo_url?.trim() ?? '';
      const prov = input.owner_name?.trim()
        ? input.provider ?? 'gitea'
        : providerForRepoUrl(repoUrl);
      if (gitMode === 'draft_pr' && !prov) {
        throw badRequest(
          "git_mode 'draft_pr' requires a provider repository (owner/name); raw repos are read-only",
        );
      }
      // Guardrail: the project's provider_allowlist restricts which git hosts a
      // service may target (400 provider_not_allowed; raw => the "raw" sentinel).
      if (!providerAllowed(p.provider_allowlist, prov ?? undefined)) {
        const label = prov ?? 'raw';
        throw new ApiError(
          400,
          `this project's guardrails do not allow ${label} repositories`,
          { error: { code: 'provider_not_allowed', message: `provider ${label} not allowed` } },
        );
      }
      const svc: Service = {
        id: genId('svc'),
        project_id: projectId,
        name,
        repo_kind: prov ? 'provider' : 'raw',
        provider: prov ?? undefined,
        repo_owner_name: prov
          ? input.owner_name?.trim() || ownerName(repoUrl)
          : undefined,
        raw_repo_url: prov ? undefined : repoUrl,
        default_branch: input.default_branch?.trim() || 'main',
        git_mode: gitMode,
        created_at: nowISO(),
      };
      list.push(svc);
      services.set(projectId, list);
      return delay(svc);
    },

    async listProviderRepos(provider: string, q?: string) {
      // Demo: a small static gitea catalogue; other providers report "not
      // linked" the same way the orchestrator does (403).
      if (provider !== 'gitea') {
        throw new ApiError(403, `no ${provider} credential available — link your ${provider} account first`);
      }
      const all = [
        { id: 101, full_name: 'acme/demo', description: 'Demo web app', default_branch: 'main', private: false },
        { id: 102, full_name: 'acme/api', description: 'Backend API', default_branch: 'main', private: true },
        { id: 103, full_name: 'jcloud/seed', description: 'Seed repository', default_branch: 'main', private: false },
      ];
      const needle = (q ?? '').trim().toLowerCase();
      return delay(needle ? all.filter((r) => r.full_name.toLowerCase().includes(needle)) : all);
    },

    async createServiceRun(serviceId: string, input: CreateRunInput) {
      let projectId: string | undefined;
      let svc: Service | undefined;
      for (const [pid, list] of services) {
        const found = list.find((s) => s.id === serviceId);
        if (found) {
          projectId = pid;
          svc = found;
          break;
        }
      }
      if (!projectId || !svc) throw new ApiError(404, 'service not found');
      // Guardrail: honour the project's provider_allowlist at dispatch (it may have
      // tightened since the service was created) — 403 provider_not_allowed.
      const proj = projects.get(projectId);
      if (!providerAllowed(proj?.provider_allowlist, svc.provider)) {
        const label = svc.provider ?? 'raw';
        throw new ApiError(
          403,
          `this project's guardrails do not allow running on ${label} repositories`,
          { error: { code: 'provider_not_allowed', message: `provider ${label} not allowed` } },
        );
      }
      return delay(publicRun(makeRun(projectId, serviceId, input.prompt)));
    },

    /* ---- members (blueprint §2) ------------------------------------------- */
    async listMembers(projectId: string) {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      return delay([...(members.get(projectId) ?? [])]);
    },

    async addMember(projectId: string, input: AddMemberInput) {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      const target =
        DEMO_USERS.find((u) => u.id === input.user_id) ??
        DEMO_USERS.find(
          (u) =>
            !!input.username &&
            u.display_name.toLowerCase().includes(input.username.toLowerCase()),
        );
      if (!target) throw new ApiError(404, 'user not found');
      const list = members.get(projectId) ?? [];
      const existing = list.find((m) => m.user_id === target.id);
      const member = asMember(target, input.role);
      if (existing) existing.role = input.role;
      else list.push(member);
      members.set(projectId, list);
      return delay(member);
    },

    async removeMember(projectId: string, userId: string) {
      const list = members.get(projectId) ?? [];
      const target = list.find((m) => m.user_id === userId);
      if (!target) throw new ApiError(404, 'member not found');
      if (
        target.role === 'owner' &&
        list.filter((m) => m.role === 'owner').length <= 1
      ) {
        throw new ApiError(409, 'cannot remove the last owner', {
          error: { code: 'conflict', message: 'cannot remove the last owner' },
        });
      }
      members.set(
        projectId,
        list.filter((m) => m.user_id !== userId),
      );
      await new Promise((r) => setTimeout(r, ms(120)));
    },

    async searchUsers(q: string) {
      const needle = q.trim().toLowerCase();
      const out = DEMO_USERS.filter(
        (u) => !needle || u.display_name.toLowerCase().includes(needle),
      ).slice(0, 20);
      return delay(out);
    },
  };
}
