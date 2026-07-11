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
  ApiKey,
  CreateApiKeyInput,
  CreateApiKeyResponse,
  CreateKanbanLinkInput,
  CreateProjectInput,
  CreateRunInput,
  CreateScheduleInput,
  CreateServiceInput,
  FailureReason,
  CreateModelInput,
  CreateIntegrationInput,
  Integration,
  KanbanClusterConfig,
  KanbanConnectStart,
  KanbanConnectStatus,
  KanbanLink,
  Me,
  Member,
  MemberRole,
  Model,
  PrInfo,
  Project,
  ProjectModels,
  ReviewRunSummary,
  Run,
  RunArtifact,
  RunEvent,
  RunEventType,
  RunMessage,
  RunPermission,
  RunStatus,
  Schedule,
  Service,
  SystemInfo,
  UpdateIntegrationInput,
  UpdateKanbanConfigInput,
  UpdateModelInput,
  UpdateProjectInput,
  UpdateScheduleInput,
  UpdateServiceInput,
  UserSearchResult,
} from './types';
import { providerForRepoUrl } from '../lib/repo';
import { isReservedEnvKey, isValidEnvKey } from '../lib/env';

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

/**
 * cronError mirrors the orchestrator's schedule cron gate (F11 / D24) closely
 * enough for demo/e2e: it throws a typed `invalid_cron` / `cron_too_frequent`
 * ApiError so the UI exercises the fail-visible path. It is a LIGHTWEIGHT check
 * (5 fields, and a crude minute-cadence guard for the common every-minute and
 * step patterns) — the authoritative validation is the Go robfig/cron parser.
 */
function cronError(expr: string): void {
  const fields = expr.trim().split(/\s+/);
  const invalid = (message: string): ApiError =>
    new ApiError(400, message, { error: { code: 'invalid_cron', message } });
  if (fields.length !== 5) {
    throw invalid('cron_expr must be a valid 5-field cron expression');
  }
  const minute = fields[0] ?? '';
  // Reject expressions that would fire more than once every 5 minutes (the
  // server's min-interval guard). Only the obvious minute-field cases are caught
  // here; the real parser is exhaustive.
  const stepMatch = minute.match(/^\*\/(\d+)$/);
  const tooFrequent =
    minute === '*' ||
    (stepMatch && Number(stepMatch[1] ?? '0') < 5) ||
    /^(\d+,)+\d+$/.test(minute); // an explicit list like "0,1" can be sub-5-minutes
  if (tooFrequent) {
    const message =
      'cron fires too frequently: the minimum interval between scheduled runs is 5 minutes';
    throw new ApiError(400, message, { error: { code: 'cron_too_frequent', message } });
  }
}

interface StoredRun extends Run {
  _events: RunEvent[];
  _diff?: string;
  /** For a kind=review run: the agent run id whose PR it reviews. */
  _reviewFor?: string;
  _timers: ReturnType<typeof setTimeout>[];
  _subs: Set<(ev: RunEvent) => void>;
  _statusSubs: Set<(run: Run) => void>;
  /** F8b: this run's permission-request ledger, keyed by request_id. */
  _perms: Map<string, RunPermission>;
  /** F8b: continues the paused playback once a permission is allowed. */
  _permContinue?: (allowed: boolean) => void;
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
  // D27: the cluster jtype config, a single mutable DB-override "row" (null = no
  // override). The demo rig has no JTYPE_* env fallback, so an absent row resolves
  // to source=none / off — set one here and it becomes source=db / on (no restart),
  // mirroring the resolver so the console edit flow roundtrips.
  let kanbanCfg: { base_url: string; token_set: boolean; token_expires_at?: string } | null = null;
  function kanbanConfigView(): KanbanClusterConfig {
    if (kanbanCfg) {
      return {
        base_url: kanbanCfg.base_url,
        token_set: kanbanCfg.token_set,
        source: 'db',
        effective_enabled: true,
        effective_base_url: kanbanCfg.base_url,
        cluster_token_set: kanbanCfg.token_set,
        poll_interval: '15s',
        // D28: present only when the fallback token was minted by the device flow.
        ...(kanbanCfg.token_expires_at ? { token_expires_at: kanbanCfg.token_expires_at } : {}),
      };
    }
    // No DB row and no env fallback in the demo rig ⇒ off.
    return {
      base_url: '',
      token_set: false,
      source: 'none',
      effective_enabled: false,
      effective_base_url: '',
      cluster_token_set: false,
      poll_interval: '15s',
    };
  }

  // D28: the "Connect with jtype" device-flow registry, keyed by opaque
  // connect_id. Each record remembers its target surface (cluster fallback token,
  // or a specific link) and a poll counter: the demo/e2e roundtrip auto-approves
  // on the SECOND poll (the first is `pending`), mirroring a user tapping Approve
  // in jtype's browser page. On completion we seal a fake 90-day token into the
  // target (token_set + token_expires_at) so the credential badge + expiry flip
  // exactly as they would against a real orchestrator — no plaintext ever crosses.
  type ConnectTarget = { kind: 'cluster' } | { kind: 'link'; projectId: string; linkId: string };
  interface ConnectRecord {
    target: ConnectTarget;
    polls: number;
    tokenExpiresAt?: string;
  }
  const connects = new Map<string, ConnectRecord>();
  const DEVICE_TOKEN_TTL_MS = 90 * 24 * 60 * 60 * 1000; // MCP_TOKEN_TTL_SECS = 90d
  function sixDigitCode(): string {
    return String(Math.floor(100000 + Math.random() * 900000));
  }
  function startConnect(target: ConnectTarget, baseUrl: string): KanbanConnectStart {
    const connectId = genId('kc');
    const userCode = sixDigitCode();
    connects.set(connectId, { target, polls: 0 });
    return {
      connect_id: connectId,
      user_code: userCode,
      verification_uri: `${baseUrl}/oauth/device`,
      verification_uri_complete: `${baseUrl}/oauth/device?code=${userCode}`,
      expires_in: 600,
      interval: 2,
    };
  }
  function pollConnect(connectId: string): KanbanConnectStatus {
    const rec = connects.get(connectId);
    if (!rec) {
      throw new ApiError(404, 'connect flow expired', {
        error: { code: 'connect_expired', message: 'connect flow expired or unknown' },
      });
    }
    rec.polls += 1;
    // Still waiting for the user to approve in jtype's browser page.
    if (rec.polls < 2) return { status: 'pending', token_set: false };
    // Approved: seal a fresh 90-day token into the target (idempotent across
    // repeat polls — the expiry is fixed on first completion).
    if (!rec.tokenExpiresAt) rec.tokenExpiresAt = nowISO(DEVICE_TOKEN_TTL_MS);
    if (rec.target.kind === 'cluster') {
      if (kanbanCfg) {
        kanbanCfg.token_set = true;
        kanbanCfg.token_expires_at = rec.tokenExpiresAt;
      }
    } else {
      const l = kanbanLinks.get(rec.target.linkId);
      if (l) {
        l.token_set = true;
        l.credential_status = 'per_link';
        l.token_expires_at = rec.tokenExpiresAt;
        kanbanLinks.set(l.id, l);
      }
    }
    return { status: 'complete', token_set: true, token_expires_at: rec.tokenExpiresAt };
  }

  // F11 / D24: schedules (service cron triggers), keyed by schedule id.
  const schedules = new Map<string, Schedule>();
  // D19 / F5: project integrations, keyed by project id.
  const integrations = new Map<string, Integration[]>();
  // F12 / D24: project-scoped API keys, keyed by project id. The plaintext is
  // never stored here beyond the create call's return value — only the safe
  // ApiKey fields persist, mirroring the orchestrator's hash-only storage.
  const apiKeys = new Map<string, ApiKey[]>();

  // D21: the model catalog (keyed by model id) + project grants (model id -> set
  // of project ids). Demo seeds ONE model, granted to every seeded project, so
  // the composer is enabled and a run with no explicit pick auto-selects the sole
  // grant (mirroring the orchestrator's resolution chain). A cluster admin can add
  // more on the Cluster page to exercise the multi-model pick.
  const models = new Map<string, Model>();
  const modelGrants = new Map<string, Set<string>>();
  function seedModel(id: string, name: string, model: string): void {
    models.set(id, {
      id,
      name,
      base_url: `http://mockllm.jcloud.svc.cluster.local:8081/v1`,
      model_name: model,
      api_key_set: true,
      created_at: nowISO(),
      updated_at: nowISO(),
      updated_by: 'demo-admin',
      granted_project_ids: [],
    });
    modelGrants.set(id, new Set());
  }
  seedModel('mdl_gpt4o', 'GPT-4o (mock)', 'openai/gpt-4o');

  /** Recompute a model's granted_project_ids from the grant set (view helper). */
  function modelView(m: Model): Model {
    return { ...m, granted_project_ids: [...(modelGrants.get(m.id) ?? [])].sort() };
  }
  function grantAllModelsTo(projectId: string): void {
    for (const set of modelGrants.values()) set.add(projectId);
  }

  /** Ids of models granted to a project. */
  function grantedModelIds(projectId: string): string[] {
    return [...models.keys()].filter((id) => modelGrants.get(id)?.has(projectId));
  }

  /**
   * resolveModelForRun mirrors the orchestrator's D21 chain: a composer pick must
   * be granted (else 403 model_not_granted); otherwise the service default, then
   * the sole grant; several grants + no default is 409 model_not_selected; zero
   * grants is 409 model_not_configured. Returns the chosen id (null never occurs
   * in the demo — the catalog is always populated).
   */
  function resolveModelForRun(
    projectId: string,
    svc: Service,
    requested?: string,
  ): string | null {
    const granted = grantedModelIds(projectId);
    const grantedSet = new Set(granted);
    if (requested) {
      if (!grantedSet.has(requested)) {
        throw new ApiError(403, 'the selected model is not authorized for this project', {
          error: { code: 'model_not_granted', message: 'model not granted' },
        });
      }
      return requested;
    }
    if (svc.default_model_id && grantedSet.has(svc.default_model_id)) {
      return svc.default_model_id;
    }
    if (granted.length === 1) return granted[0]!;
    if (granted.length >= 2) {
      throw new ApiError(
        409,
        'several models are available — pick one for this run or set a default model on the service',
        { error: { code: 'model_not_selected', message: 'pick a model' } },
      );
    }
    throw new ApiError(
      409,
      'no LLM is configured for this project — contact a cluster admin to grant a model',
      { error: { code: 'model_not_configured', message: 'no model granted' } },
    );
  }

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
    // D21: authorize the demo catalog for every project so the composer works.
    grantAllModelsTo(p.id);
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
      // F9b: a session run reports its ACP session once established (session/new
      // for a fresh run; session/load — resumed=true — for a resume run whose
      // acp_session_id was copied at creation). Drives the "Session established/
      // resumed" system row and makes the finished run resumable in demo.
      if (run.session) {
        if (!run.acp_session_id) run.acp_session_id = genId('acp');
        emit(run, 'run.session', {
          acp_session_id: run.acp_session_id,
          resumed: run.resumed_from != null,
        });
      }
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
    // The edit + wrap-up tail, schedulable from any point in time (the F8b
    // approval pause below re-schedules it relative to the user's answer).
    //
    // ST-1 demo: showcase the Gitea draft-PR closed loop. The runner pushes the
    // agent/run-<id> branch (run.git), then the diff artifact lands, then the run
    // succeeds — and the draft PR link is populated on the SAME succeeded frame so
    // the "Draft PR #N" chip is present the moment the header goes terminal. (The
    // real orchestrator opens the PR just after success; the console likewise
    // picks up pr_url via its terminal refetch — this keeps the demo showcase
    // deterministic without depending on a post-terminal stream frame.)
    const editAndFinish = (base: number) => {
      schedule(run, base, () => {
        emit(run, 'agent.tool_call', {
          tool: 'edit_file',
          call_id: 'c2',
          args: {
            path: 'README.md',
            instruction: 'Append "Hello" as a new final line.',
          },
        });
      });
      schedule(run, base + 900, () => {
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
      const branch = `agent/run-${run.id}`;
      schedule(run, base + 1600, () => {
        emit(run, 'run.git', { branch, commit_sha: 'a1b2c3d4' });
      });
      schedule(run, base + 1800, () => {
        run._diff = SAMPLE_DIFF;
        emit(run, 'run.artifact', { kind: 'diff' });
        // Populate the draft PR BEFORE the terminal transition so the run object the
        // terminal refetch reads (mock getRun) already carries pr_url — the chip is
        // present the moment the header goes terminal.
        run.pr_url = 'https://gitea.local/jcloud/seed/pulls/42';
        run.pr_number = 42;
        // D22: a session run parks in awaiting_input (waiting for the user's next
        // message) instead of finishing — sendMessage/finishSession drive it on.
        setStatus(run, run.session ? 'awaiting_input' : 'succeeded');
      });
    };

    if (run.permission_mode !== 'approval') {
      editAndFinish(4200);
      return;
    }

    // F8b approval mode: pause BEFORE the edit and forward a permission request
    // (mirrors acpdrive forwarding jcode's RequestPermission). Playback resumes
    // when respondPermission answers it, or timeout-denies after a while —
    // never silently auto-approves (the whole point of the mode).
    schedule(run, 4200, () => {
      const requestId = genId('permreq');
      const row: RunPermission = {
        request_id: requestId,
        run_id: run.id,
        tool_call_id: 'c2',
        title: 'Edit README.md (append "Hello")',
        options: [
          { option_id: 'allow_once', name: 'Allow', kind: 'allow_once' },
          { option_id: 'reject_once', name: 'Reject', kind: 'reject_once' },
        ],
        created_at: nowISO(),
      };
      run._perms.set(requestId, row);
      emit(run, 'agent.permission_request', {
        request_id: row.request_id,
        tool_call_id: row.tool_call_id,
        title: row.title,
        options: row.options,
      });
      run._permContinue = (allowed) => {
        if (allowed) {
          emit(run, 'agent.text', { text: 'Permission granted — applying the edit. ' });
          editAndFinish(600);
        } else {
          emit(run, 'agent.text', {
            text: 'Understood — I will not make that change. Tell me how to proceed.',
          });
          schedule(run, 600, () => setStatus(run, 'awaiting_input'));
        }
      };
      // Timeout-deny (mirrors the runner's PERMISSION_TIMEOUT_SECONDS): an
      // unanswered request resolves as {resolution: "timeout"} on the
      // reject-kind option and the session continues without the action.
      schedule(run, 45_000, () => {
        if (row.decided_at || row.resolved_at) return; // answered in time
        row.resolved_option_id = 'reject_once';
        row.resolution = 'timeout';
        row.resolved_at = nowISO();
        emit(run, 'agent.permission_resolved', {
          request_id: row.request_id,
          option_id: 'reject_once',
          resolution: 'timeout',
        });
        const cont = run._permContinue;
        run._permContinue = undefined;
        cont?.(false);
      });
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
      _perms,
      _permContinue,
      ...pub
    } = run;
    void _events;
    void _diff;
    void _reviewFor;
    void _timers;
    void _subs;
    void _statusSubs;
    void _perms;
    void _permContinue;
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
    session = false,
    permissionMode: 'approval' | '' = '',
    resumedFrom?: string,
    acpSessionId?: string,
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
      // F9b: a resume run links back to the original + carries the copied ACP
      // session id so the run.session event replays resumed=true.
      resumed_from: resumedFrom ?? null,
      acp_session_id: acpSessionId,
      created_at: nowISO(),
      started_at: null,
      finished_at: null,
      pr_url: null,
      pr_number: null,
      origin: 'api',
      session,
      permission_mode: permissionMode,
      _events: [],
      _timers: [],
      _subs: new Set(),
      _statusSubs: new Set(),
      _perms: new Map(),
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
      _perms: new Map(),
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
        // Deprecated (D20 / F5): git-host policy is a cluster allowlist +
        // integrations now; a PATCH carrying it is a typed 400 deprecated_key.
        throw new ApiError(
          400,
          'provider_allowlist is deprecated: git-host policy is now a cluster-level allowlist enforced when creating a project integration',
          { error: { code: 'deprecated_key', message: 'provider_allowlist is deprecated' } },
        );
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
      // Retry preserves the run's identity (D22/F8b): session-ness and the
      // permission mode carry over, mirroring the orchestrator.
      return delay(
        publicRun(
          makeRun(
            orig.project_id,
            orig.service_id,
            orig.prompt,
            orig.id,
            (orig.attempt ?? 1) + 1,
            orig.session === true,
            orig.permission_mode === 'approval' ? 'approval' : '',
          ),
        ),
      );
    },

    // ---- session resume (F9b / D23 ①②) ------------------------------------
    async resumeSession(runId: string, prompt: string): Promise<Run> {
      const orig = runs.get(runId);
      if (!orig) throw new ApiError(404, 'run not found');
      const conflict = (code: string, message: string) =>
        new ApiError(409, message, { error: { code, message } });
      // Mirror the orchestrator's precondition order + typed 409 codes so the
      // demo/e2e surface the same readable messages the console renders.
      if (!orig.session) {
        throw conflict(
          'run_not_resumable',
          'this run is not a multi-turn session, so there is no session to resume',
        );
      }
      if (!['succeeded', 'failed', 'canceled'].includes(orig.status)) {
        throw conflict(
          'run_not_resumable',
          'the session is still active — use the message box to continue it instead of starting a new one',
        );
      }
      if (!orig.acp_session_id) {
        throw conflict(
          'session_not_recorded',
          'this session never recorded an agent session id, so it cannot be resumed',
        );
      }
      const trimmed = prompt.trim();
      if (!trimmed) throw badRequest('prompt is required');
      // The demo assumes the cluster persistent-workspace switch is ON, so the
      // workspace_not_persistent 409 is a real-cluster-only path (not modelled).
      const run = makeRun(
        orig.project_id,
        orig.service_id,
        trimmed,
        undefined,
        1,
        true,
        orig.permission_mode === 'approval' ? 'approval' : '',
        orig.id,
        orig.acp_session_id,
      );
      return delay(publicRun(run));
    },

    // ---- multi-turn session (D22) ------------------------------------------
    async sendMessage(runId: string, prompt: string): Promise<RunMessage> {
      const r = runs.get(runId);
      if (!r) throw new ApiError(404, 'run not found');
      // Mirror the orchestrator gate: session + {awaiting_input, running} only.
      if (!r.session || !['awaiting_input', 'running'].includes(r.status)) {
        throw new ApiError(409, 'the session is not accepting messages', {
          error: { code: 'run_not_awaiting', message: 'the session is not accepting messages' },
        });
      }
      const trimmed = prompt.trim();
      if (!trimmed) throw badRequest('prompt is required');
      // Timeline bubble + a canned agent reply, then park awaiting_input again.
      emit(r, 'user.message', { prompt: trimmed, by: DEMO_ME.user.display_name });
      setStatus(r, 'running');
      schedule(r, 900, () => {
        emit(r, 'agent.text', { text: `Continuing on it: ${trimmed}` });
      });
      schedule(r, 1800, () => setStatus(r, 'awaiting_input'));
      const msg: RunMessage = {
        id: genId('msg'),
        run_id: runId,
        seq: 1,
        prompt: trimmed,
        created_at: nowISO(),
        delivered_at: null,
      };
      return delay(msg);
    },

    // F8b: answer a pending permission request. Mirrors the orchestrator's
    // validation order (404 unknown → 409 already answered/expired → 400
    // foreign option) and resolves the request shortly after — the gap is the
    // real system's decision-poll latency, and it exercises the console's
    // optimistic "decided, waiting for the agent" card state.
    async respondPermission(runId: string, requestId: string, optionId: string): Promise<RunPermission> {
      const r = runs.get(runId);
      if (!r) throw new ApiError(404, 'run not found');
      const perm = r._perms.get(requestId);
      if (!perm) {
        throw new ApiError(404, 'permission request not found', {
          error: { code: 'not_found', message: 'permission request not found' },
        });
      }
      if (perm.decided_at || perm.resolved_at) {
        throw new ApiError(409, 'this permission request has already been answered or has expired', {
          error: {
            code: 'permission_already_resolved',
            message: 'this permission request has already been answered or has expired',
          },
        });
      }
      const opt = perm.options.find((o) => o.option_id === optionId);
      if (!opt) {
        throw new ApiError(400, 'option_id is not one of the options this request offered', {
          error: {
            code: 'invalid_option',
            message: 'option_id is not one of the options this request offered',
          },
        });
      }
      perm.decided_option_id = optionId;
      perm.decided_by = DEMO_ME.user.id ?? 'demo-user';
      perm.decided_at = nowISO();
      // The "runner" picks the decision up on its next poll and resolves.
      schedule(r, 600, () => {
        perm.resolved_option_id = optionId;
        perm.resolution = 'user';
        perm.resolved_at = nowISO();
        emit(r, 'agent.permission_resolved', {
          request_id: requestId,
          option_id: optionId,
          resolution: 'user',
        });
        const cont = r._permContinue;
        r._permContinue = undefined;
        cont?.(opt.kind.toLowerCase().includes('allow'));
      });
      return delay({ ...perm });
    },

    async finishSession(runId: string): Promise<Run> {
      const r = runs.get(runId);
      if (!r) throw new ApiError(404, 'run not found');
      if (!r.session) {
        throw new ApiError(409, 'this run is not a multi-turn session', {
          error: { code: 'run_not_awaiting', message: 'not a session' },
        });
      }
      if (!['succeeded', 'failed', 'canceled'].includes(r.status)) {
        emit(r, 'session.finish', { reason: 'user', by: DEMO_ME.user.display_name });
        for (const t of r._timers) clearTimeout(t);
        r._timers = [];
        setStatus(r, 'succeeded');
      }
      return delay(publicRun(r));
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
          allowed_git_hosts: ['gitea.jcloud.svc.cluster.local', 'github.com'],
        },
        runner: { image: 'ghcr.io/jcloud/runner:demo' },
        namespace: 'jcloud',
        launcher: 'kubernetes',
        auth: {
          providers: ['gitea'],
          users_count: DEMO_USERS.length,
        },
        kanban: (() => {
          // D27: reflect the mutable cluster config + its source so the demo edit
          // flow roundtrips (set base_url on the Cluster page → snapshot flips on).
          const kc = kanbanConfigView();
          return {
            enabled: kc.effective_enabled,
            base_url: kc.effective_base_url,
            poll_interval: kc.poll_interval,
            source: kc.source,
          };
        })(),
      };
      return delay(info);
    },

    /* ---- model catalog + project grants (D21) ----------------------------- */
    async listModels(): Promise<Model[]> {
      return delay([...models.values()].map(modelView).reverse());
    },

    async createModel(input: CreateModelInput): Promise<Model> {
      // Mirror the orchestrator's validation. AUTHORITATIVE rules live in
      // orchestrator/internal/api/models.go (validateBaseURL / validateModelName).
      const name = input.name?.trim() ?? '';
      if (!name) throw badRequest('name is required');
      const base = input.base_url?.trim() ?? '';
      if (!/^https?:\/\/.+/i.test(base)) throw badRequest('base_url must be an http(s) URL');
      const model = input.model_name?.trim() ?? '';
      const [provider, ...rest] = model.split('/');
      if (!provider || rest.join('/') === '') {
        throw badRequest("model_name must be in 'provider/model' form");
      }
      for (const m of models.values()) {
        if (m.name === name) {
          throw new ApiError(409, `a model named '${name}' already exists`, {
            error: { code: 'conflict', message: `model '${name}' exists` },
          });
        }
      }
      const id = genId('mdl');
      const m: Model = {
        id, name, base_url: base, model_name: model, api_key_set: !!input.api_key,
        created_at: nowISO(), updated_at: nowISO(), updated_by: 'demo-admin', granted_project_ids: [],
      };
      models.set(id, m);
      modelGrants.set(id, new Set());
      return delay(modelView(m));
    },

    async updateModel(id: string, input: UpdateModelInput): Promise<Model> {
      const m = models.get(id);
      if (!m) throw new ApiError(404, 'model not found');
      if (input.name !== undefined) {
        const name = input.name.trim();
        if (!name) throw badRequest('name cannot be empty');
        for (const other of models.values()) {
          if (other.id !== id && other.name === name) {
            throw new ApiError(409, `a model named '${name}' already exists`, {
              error: { code: 'conflict', message: `model '${name}' exists` },
            });
          }
        }
        m.name = name;
      }
      if (input.base_url !== undefined) {
        if (!/^https?:\/\/.+/i.test(input.base_url.trim())) throw badRequest('base_url must be an http(s) URL');
        m.base_url = input.base_url.trim();
      }
      if (input.model_name !== undefined) {
        const model = input.model_name.trim();
        const [provider, ...rest] = model.split('/');
        if (!provider || rest.join('/') === '') throw badRequest("model_name must be in 'provider/model' form");
        m.model_name = model;
      }
      if (input.api_key !== undefined) m.api_key_set = input.api_key !== '';
      m.updated_at = nowISO();
      models.set(id, m);
      return delay(modelView(m));
    },

    async deleteModel(id: string): Promise<void> {
      if (!models.delete(id)) throw new ApiError(404, 'model not found');
      modelGrants.delete(id);
      // Null any service default referencing it (mirrors ON DELETE SET NULL).
      for (const list of services.values()) {
        for (const s of list) if (s.default_model_id === id) s.default_model_id = null;
      }
      return delay(undefined);
    },

    async grantModel(modelId: string, projectId: string): Promise<Model> {
      const m = models.get(modelId);
      if (!m || !projects.has(projectId)) throw new ApiError(404, 'model or project not found');
      (modelGrants.get(modelId) ?? new Set()).add(projectId);
      return delay(modelView(m));
    },

    async revokeModel(modelId: string, projectId: string): Promise<Model> {
      const m = models.get(modelId);
      if (!m) throw new ApiError(404, 'model not found');
      modelGrants.get(modelId)?.delete(projectId);
      return delay(modelView(m));
    },

    async listProjectModels(projectId: string): Promise<ProjectModels> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      const granted = [...models.values()]
        .filter((m) => modelGrants.get(m.id)?.has(projectId))
        .map((m) => ({ id: m.id, name: m.name, model_name: m.model_name }));
      return delay({ models: granted, env_fallback: false });
    },

    /* ---- kanban links (Feature E / F6) ------------------------------------ */
    // Cluster-admin READ-ONLY overview across all projects.
    async listKanbanLinks(): Promise<KanbanLink[]> {
      return delay([...kanbanLinks.values()]);
    },
    // A project's links (owner-managed).
    async listProjectKanbanLinks(projectId: string): Promise<KanbanLink[]> {
      return delay([...kanbanLinks.values()].filter((l) => l.project_id === projectId));
    },
    async createProjectKanbanLink(
      projectId: string,
      input: CreateKanbanLinkInput,
    ): Promise<KanbanLink> {
      const ws = input.workspace_id?.trim();
      const board = input.board_ref?.trim();
      if (!ws || !board || !input.service_id || !input.trigger_column?.trim()) {
        throw badRequest('workspace_id, board_ref, service_id and trigger_column are required');
      }
      // The service must belong to this project.
      const svcs = services.get(projectId) ?? [];
      if (!svcs.some((s) => s.id === input.service_id)) {
        throw badRequest('service does not belong to this project');
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
        project_id: projectId,
        service_id: input.service_id,
        trigger_column: input.trigger_column.trim(),
        done_column: input.done_column?.trim() || undefined,
        enabled: true,
        token_set: !!input.token?.trim(),
        // The demo rig has no cluster JTYPE_TOKEN, so a tokenless link is
        // honestly "missing" (mirrors the server derivation; exercises the
        // error badge in demo mode).
        credential_status: input.token?.trim() ? 'per_link' : 'missing',
        created_at: new Date().toISOString(),
      };
      kanbanLinks.set(link.id, link);
      return delay(link);
    },
    async updateProjectKanbanLinkToken(
      projectId: string,
      linkId: string,
      token: string,
    ): Promise<KanbanLink> {
      const l = kanbanLinks.get(linkId);
      if (!l || l.project_id !== projectId) throw new ApiError(404, 'kanban link not found');
      l.token_set = !!token.trim();
      l.credential_status = token.trim() ? 'per_link' : 'missing';
      // A manual PAT (or a clear) has unknown expiry — drop any device-flow expiry.
      l.token_expires_at = undefined;
      kanbanLinks.set(linkId, l);
      return delay({ ...l });
    },
    async deleteProjectKanbanLink(projectId: string, linkId: string): Promise<void> {
      const l = kanbanLinks.get(linkId);
      if (!l || l.project_id !== projectId) throw new ApiError(404, 'kanban link not found');
      kanbanLinks.delete(linkId);
      return delay(undefined);
    },

    /* ---- cluster kanban config (D27) -------------------------------------- */
    async getKanbanConfig(): Promise<KanbanClusterConfig> {
      return delay(kanbanConfigView());
    },
    async updateKanbanConfig(input: UpdateKanbanConfigInput): Promise<KanbanClusterConfig> {
      // Mirror the orchestrator's validateBaseURL gate (400 on a non-http(s) URL).
      const base = input.base_url?.trim() ?? '';
      if (!/^https?:\/\/.+/i.test(base)) throw badRequest('base_url must be an http(s) URL');
      // Three-state token: omitted keeps the stored token_set; "" clears; a value
      // sets/rotates. The demo assumes AUTH_TOKEN_KEY is configured (so a token
      // write never 409s here — that path is a real-cluster-only concern).
      const prevTokenSet = kanbanCfg?.token_set ?? false;
      const tokenSet = input.token !== undefined ? input.token !== '' : prevTokenSet;
      kanbanCfg = { base_url: base, token_set: tokenSet };
      return delay(kanbanConfigView());
    },
    async deleteKanbanConfig(): Promise<KanbanClusterConfig> {
      kanbanCfg = null;
      return delay(kanbanConfigView());
    },

    /* ---- kanban "Connect with jtype" device flow (D28) -------------------- */
    async startKanbanConnect(): Promise<KanbanConnectStart> {
      // Cluster connect requires a saved DB base_url (D27 same-source binding) —
      // fail-visible, mirroring the orchestrator's 409 base_url_not_configured.
      if (!kanbanCfg || !kanbanCfg.base_url) {
        throw new ApiError(409, 'save the jtype base URL before connecting', {
          error: {
            code: 'base_url_not_configured',
            message: 'Save the jtype base URL before connecting.',
          },
        });
      }
      return delay(startConnect({ kind: 'cluster' }, kanbanCfg.base_url));
    },
    async pollKanbanConnect(connectId: string): Promise<KanbanConnectStatus> {
      return delay(pollConnect(connectId));
    },
    async startLinkConnect(projectId: string, linkId: string): Promise<KanbanConnectStart> {
      const l = kanbanLinks.get(linkId);
      if (!l || l.project_id !== projectId) throw new ApiError(404, 'kanban link not found');
      // Per-link connect needs the cluster integration effective (else the minted
      // token has no jtype to talk to) — 409 kanban_not_configured, fail-visible.
      const eff = kanbanConfigView();
      if (!eff.effective_enabled) {
        throw new ApiError(409, 'the cluster jtype integration is not configured', {
          error: {
            code: 'kanban_not_configured',
            message: 'Ask a cluster admin to configure jtype on the Cluster page first.',
          },
        });
      }
      return delay(startConnect({ kind: 'link', projectId, linkId }, eff.effective_base_url));
    },
    async pollLinkConnect(
      projectId: string,
      linkId: string,
      connectId: string,
    ): Promise<KanbanConnectStatus> {
      const l = kanbanLinks.get(linkId);
      if (!l || l.project_id !== projectId) throw new ApiError(404, 'kanban link not found');
      return delay(pollConnect(connectId));
    },

    /* ---- schedules (F11 / D24) -------------------------------------------- */
    async listServiceSchedules(serviceId: string): Promise<Schedule[]> {
      return delay(
        [...schedules.values()]
          .filter((sc) => sc.service_id === serviceId)
          .sort((a, b) => (a.created_at < b.created_at ? 1 : -1)),
      );
    },
    async createServiceSchedule(
      serviceId: string,
      input: CreateScheduleInput,
    ): Promise<Schedule> {
      // The service must exist somewhere in the demo store.
      let found = false;
      for (const list of services.values()) if (list.some((s) => s.id === serviceId)) found = true;
      if (!found) throw new ApiError(404, 'service not found');
      const cron = input.cron_expr?.trim();
      const prompt = input.prompt?.trim();
      if (!cron || !prompt) throw badRequest('cron_expr and prompt are required');
      cronError(cron); // throws invalid_cron / cron_too_frequent, mirroring the server
      const now = new Date().toISOString();
      const sc: Schedule = {
        id: 'sc-' + Math.random().toString(36).slice(2, 10),
        service_id: serviceId,
        cron_expr: cron,
        prompt,
        enabled: input.enabled ?? true,
        last_fired_at: null,
        last_error: '',
        created_at: now,
        updated_at: now,
      };
      schedules.set(sc.id, sc);
      return delay(sc);
    },
    async updateSchedule(scheduleId: string, input: UpdateScheduleInput): Promise<Schedule> {
      const sc = schedules.get(scheduleId);
      if (!sc) throw new ApiError(404, 'schedule not found');
      if (input.cron_expr !== undefined) {
        const cron = input.cron_expr.trim();
        if (!cron) throw badRequest('cron_expr cannot be empty');
        cronError(cron);
        sc.cron_expr = cron;
      }
      if (input.prompt !== undefined) {
        const prompt = input.prompt.trim();
        if (!prompt) throw badRequest('prompt cannot be empty');
        sc.prompt = prompt;
      }
      if (input.enabled !== undefined) sc.enabled = input.enabled;
      sc.updated_at = new Date().toISOString();
      schedules.set(scheduleId, sc);
      return delay({ ...sc });
    },
    async deleteSchedule(scheduleId: string): Promise<void> {
      if (!schedules.delete(scheduleId)) throw new ApiError(404, 'schedule not found');
      return delay(undefined);
    },

    /* ---- integrations (D19 / F5) ------------------------------------------ */
    async listIntegrations(projectId: string): Promise<Integration[]> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      return delay([...(integrations.get(projectId) ?? [])]);
    },
    async createIntegration(projectId: string, input: CreateIntegrationInput): Promise<Integration> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      const name = input.name?.trim() || 'default';
      const list = integrations.get(projectId) ?? [];
      if (list.some((i) => i.name === name)) {
        throw new ApiError(409, `an integration named '${name}' already exists`, {
          error: { code: 'conflict', message: 'integration name exists' },
        });
      }
      if (!input.host?.trim()) throw badRequest('host is required');
      if (!input.token?.trim()) throw badRequest('token is required');
      const integ: Integration = {
        id: genId('integ'),
        project_id: projectId,
        name,
        provider: input.provider,
        host: input.host.trim(),
        cred_type: input.cred_type?.trim() || 'pat',
        // Demo: derive a plausible bot username from the host + provider.
        bot_username: `${input.provider}-bot`,
        token_set: true,
        created_at: nowISO(),
        updated_at: nowISO(),
      };
      list.push(integ);
      integrations.set(projectId, list);
      return delay(integ);
    },
    async updateIntegration(integrationId: string, input: UpdateIntegrationInput): Promise<Integration> {
      for (const [, list] of integrations) {
        const integ = list.find((i) => i.id === integrationId);
        if (!integ) continue;
        if (input.name?.trim()) integ.name = input.name.trim();
        if (input.token !== undefined) {
          if (!input.token.trim()) throw badRequest('token cannot be empty');
          integ.token_set = true;
          integ.bot_username = `${integ.provider}-bot`; // refreshed on rotation
        }
        integ.updated_at = nowISO();
        return delay({ ...integ });
      }
      throw new ApiError(404, 'integration not found');
    },
    async deleteIntegration(integrationId: string): Promise<void> {
      for (const [pid, list] of integrations) {
        const idx = list.findIndex((i) => i.id === integrationId);
        if (idx >= 0) {
          list.splice(idx, 1);
          integrations.set(pid, list);
          // Unbind any service that referenced it.
          for (const svc of services.get(pid) ?? []) {
            if (svc.integration_id === integrationId) svc.integration_id = null;
          }
          return delay(undefined);
        }
      }
      throw new ApiError(404, 'integration not found');
    },
    async listIntegrationRepos(projectId: string, integrationId: string, q?: string) {
      const integ = (integrations.get(projectId) ?? []).find((i) => i.id === integrationId);
      if (!integ) throw new ApiError(404, 'integration not found');
      const all = [
        { id: 201, full_name: 'acme/demo', description: 'Demo web app', default_branch: 'main', private: false },
        { id: 202, full_name: 'acme/api', description: 'Backend API', default_branch: 'main', private: true },
        { id: 203, full_name: 'acme/infra', description: 'Infra as code', default_branch: 'main', private: true },
      ];
      const needle = (q ?? '').trim().toLowerCase();
      return delay(needle ? all.filter((r) => r.full_name.toLowerCase().includes(needle)) : all);
    },

    /* ---- project-scoped API keys (F12 / D24) ------------------------------- */
    async listApiKeys(projectId: string): Promise<ApiKey[]> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      return delay([...(apiKeys.get(projectId) ?? [])]);
    },
    async createApiKey(projectId: string, input: CreateApiKeyInput): Promise<CreateApiKeyResponse> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      const name = input.name?.trim();
      if (!name) throw badRequest('name is required');
      // Demo-only plaintext (not cryptographically strong — the real key comes
      // from the orchestrator's crypto/rand). Shape-compatible with "jck_"+hex
      // so the UI's prefix/copy affordances behave identically to production.
      const body = Array.from({ length: 64 }, () => Math.floor(Math.random() * 16).toString(16)).join('');
      const key = `jck_${body}`;
      const k: ApiKey = {
        id: genId('ak'),
        project_id: projectId,
        name,
        prefix: key.slice(0, 8),
        created_at: nowISO(),
        last_used_at: null,
        revoked_at: null,
      };
      const list = apiKeys.get(projectId) ?? [];
      list.unshift(k);
      apiKeys.set(projectId, list);
      return delay({ ...k, key });
    },
    async revokeApiKey(projectId: string, keyId: string): Promise<void> {
      const k = (apiKeys.get(projectId) ?? []).find((x) => x.id === keyId);
      if (!k) throw new ApiError(404, 'api key not found');
      k.revoked_at = nowISO();
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
      // Integration binding (D19 / F5): the provider comes from the integration.
      const integrationId = input.integration_id?.trim() || undefined;
      let boundProvider = prov;
      if (integrationId) {
        const integ = (integrations.get(projectId) ?? []).find((i) => i.id === integrationId);
        if (!integ) throw badRequest('integration not found in this project');
        boundProvider = integ.provider;
      }
      const svc: Service = {
        id: genId('svc'),
        project_id: projectId,
        name,
        repo_kind: boundProvider ? 'provider' : 'raw',
        provider: boundProvider ?? undefined,
        repo_owner_name: boundProvider
          ? input.owner_name?.trim() || ownerName(repoUrl)
          : undefined,
        raw_repo_url: boundProvider ? undefined : repoUrl,
        default_branch: input.default_branch?.trim() || 'main',
        git_mode: gitMode,
        integration_id: integrationId ?? null,
        created_at: nowISO(),
      };
      list.push(svc);
      services.set(projectId, list);
      return delay(svc);
    },

    async updateService(serviceId: string, input: UpdateServiceInput): Promise<Service> {
      for (const [pid, list] of services) {
        const svc = list.find((s) => s.id === serviceId);
        if (!svc) continue;
        if (input.default_model_id !== undefined) {
          const id = input.default_model_id.trim();
          if (id === '') {
            svc.default_model_id = null;
          } else {
            if (!modelGrants.get(id)?.has(pid)) {
              throw new ApiError(400, 'that model is not authorized for this project', {
                error: { code: 'model_not_granted', message: 'model not granted to project' },
              });
            }
            svc.default_model_id = id;
          }
        }
        return delay({ ...svc });
      }
      throw new ApiError(404, 'service not found');
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
      // D21 resolution chain (mirrors orchestrator selectModel): composer pick →
      // service default → the project's sole granted model → typed errors.
      const modelId = resolveModelForRun(projectId, svc, input.model_id);
      // F8b (mirrors the orchestrator gate): "approval" only rides on a session.
      if (input.permission_mode === 'approval' && input.session !== true) {
        throw badRequest('permission_mode "approval" requires session mode');
      }
      const run = makeRun(
        projectId,
        serviceId,
        input.prompt,
        undefined,
        1,
        input.session === true,
        input.permission_mode === 'approval' ? 'approval' : '',
      );
      run.model_id = modelId ?? undefined;
      return delay(publicRun(run));
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
