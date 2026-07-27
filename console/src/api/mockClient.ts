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
  JTypeCloudDocument,
  JTypeDocumentListItem,
  JTypeSaveDocumentRequest,
  JTypeSaveDocumentResponse,
} from 'jtype-board-react';
import type {
  AddMemberInput,
  ApiKey,
  BoardEmbedLink,
  CatalogModel,
  ClusterProviderConfig,
  CreateApiKeyInput,
  CreateApiKeyResponse,
  CreateProjectAutomationInput,
  CreateProjectInput,
  CreateRunInput,
  CreateServiceInput,
  FailureReason,
  CreateModelInput,
  CreateModelProviderInput,
  CreateProviderModelInput,
  Me,
  Member,
  MemberRole,
  Model,
  ModelProvider,
  ModelProviderVerification,
  ProviderModel,
  PrInfo,
  Project,
  ProjectAutomationSpec,
  ProjectPlugin,
  PluginConsentInput,
  PluginInstallStart,
  PluginRepositoryResource,
  PluginWorkspaceResource,
  PluginBoardResource,
  ProviderKind,
  SetupStatus,
  SetupInput,
  ProjectModels,
  ReviewRunSummary,
  Run,
  RunArtifact,
  RunEvent,
  RunEventType,
  RunMessage,
  RunPermission,
  RunnerPrewarm,
  ResumeSessionOptions,
  RunStatus,
  Service,
	ServiceBranch,
  SystemInfo,
  UpdateClusterProviderConfigInput,
  UpdateProjectAutomationInput,
  UpdateModelInput,
  UpdateModelProviderInput,
  UpdateProjectInput,
  UpdateProviderModelInput,
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

// Demo runner-image prewarm state: '' until the Cluster page's "sync runner
// image" button is pressed, then the sync timestamp (so the demo flow
// roundtrips: sync → snapshot shows last_sync + all nodes cached).
let prewarmSyncedAt = '';

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

/** Browser-safe repository URL projection used by the demo client. */
function mockRepoHTMLURL(provider: Service['provider'], ownerNamePath: string, rawRepoURL = ''): string | undefined {
  if (rawRepoURL) {
    try {
      const url = new URL(rawRepoURL);
      if (url.protocol === 'http:' || url.protocol === 'https:') {
        url.pathname = url.pathname.replace(/\.git$/, '');
        return url.toString().replace(/\/$/, '');
      }
    } catch {
      // Fall through to the provider demo host. Production URLs are derived by
      // the orchestrator from server-owned integration and OAuth settings.
    }
  }
  if (!ownerNamePath) return undefined;
  if (provider === 'github') return `https://github.com/${ownerNamePath}`;
  if (provider === 'gitlab') return `https://gitlab.com/${ownerNamePath}`;
  if (provider === 'gitea') return `https://gitea.example/${ownerNamePath}`;
  return undefined;
}

export function createMockClient(): ApiClient {
  const projects = new Map<string, Project>();
  const runs = new Map<string, StoredRun>();
  // Services + members keyed by project id (blueprint §1/§2). A project starts
  // with a single 'default' service — the "one repo = one project" simple UX.
  const services = new Map<string, Service[]>();
  const members = new Map<string, Member[]>();
  // The board proxy has fixture documents for offline embed tests. Service
  // Kanban Automations, not these fixtures, decide which boards a project can
  // open; plugin resource pickers are backed by their own current API below.
  type EmbeddedBoard = {
    id: string;
    ref: string;
    title: string;
    columns: Array<{ key: string; name: string }>;
  };
  const embeddedBoards: Record<string, EmbeddedBoard[]> = {
    ws_team: [
      {
        id: 'b_ab12cd34',
        ref: 'jtype.board',
        title: 'jtype',
        columns: [
          { key: 'todo', name: 'To do' },
          { key: 'ai', name: 'AI' },
          { key: 'done', name: 'Done' },
        ],
      },
      {
        id: 'b_ef56gh78',
        ref: 'Jcode.board',
        title: 'Jcode',
        columns: [
          { key: 'backlog', name: 'Backlog' },
          { key: 'agent', name: 'Agent' },
          { key: 'shipped', name: 'Shipped' },
        ],
      },
    ],
    ws_solo: [
      {
        id: 'b_solo0001',
        ref: 'personal.board',
        title: 'Personal',
        columns: [
          { key: 'inbox', name: 'Inbox' },
          { key: 'run', name: 'Run' },
        ],
      },
    ],
  };
  const projectAutomations = new Map<string, ProjectAutomationSpec>();
  const projectPlugins = new Map<string, Map<ProviderKind, ProjectPlugin>>();
  const clusterProviders = new Map<ProviderKind, ClusterProviderConfig>((['github', 'gitlab', 'gitea', 'jtype'] as const).map((provider): [ProviderKind, ClusterProviderConfig] => [provider, { provider, base_url: provider === 'github' ? 'https://github.com' : '', login_enabled: provider !== 'jtype', plugin_enabled: true, configured: false, health: 'unknown' }]));

  function pluginList(projectId: string): Map<ProviderKind, ProjectPlugin> {
    if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
    let list = projectPlugins.get(projectId);
    if (!list) {
      list = new Map<ProviderKind, ProjectPlugin>();
      for (const provider of ['github', 'gitlab', 'gitea', 'jtype'] as const) {
        list.set(provider, {
          provider,
          status: 'not_connected',
          scopes: [],
          service_count: 0,
          automation_count: 0,
        });
      }
      projectPlugins.set(projectId, list);
    }
    return list;
  }

  function copyPlugin(plugin: ProjectPlugin): ProjectPlugin {
    return { ...plugin, scopes: [...plugin.scopes], capabilities: plugin.capabilities ? {
      supported_actions: [...plugin.capabilities.supported_actions],
      unavailable_actions: plugin.capabilities.unavailable_actions?.map((row) => ({ ...row })),
    } : undefined };
  }

  function automationFromInput(
    input: CreateProjectAutomationInput,
    existing?: ProjectAutomationSpec,
  ): ProjectAutomationSpec {
    const now = nowISO();
    const triggerKind = input.scm ? 'scm' : input.kanban ? 'kanban' : 'cron';
    return {
      automation: {
        id: existing?.automation.id ?? genId('auto'),
        service_id: input.service_id,
        installation_id: input.kanban?.installation_id,
        name: input.name,
        trigger_kind: triggerKind,
        prompt_template: input.prompt_template,
        enabled: input.enabled ?? existing?.automation.enabled ?? true,
        ignore_jcode: input.ignore_jcode ?? existing?.automation.ignore_jcode ?? true,
        created_by: existing?.automation.created_by ?? 'u_ada',
        created_at: existing?.automation.created_at ?? now,
        updated_at: now,
      },
      scm: input.scm ? {
        automation_id: existing?.automation.id,
        branch: input.scm.branch,
        path_pattern: input.scm.path_pattern,
        conclusion: input.scm.conclusion,
      } : undefined,
      actions: input.scm?.actions.map((action) => ({ ...action })),
      kanban: input.kanban ? { automation_id: existing?.automation.id, ...input.kanban } : undefined,
      cron: input.cron ? { automation_id: existing?.automation.id, ...input.cron } : undefined,
    };
  }
  // D19 / F5: project integrations, keyed by project id.
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
  const modelAccountGrants = new Map<string, Set<string>>();
  const modelProviders = new Map<string, ModelProvider>();
  // M2: project-owned providers (each carries project_id + write-only headers),
  // keyed by project id. Independent of the cluster catalog above — a project
  // owns its providers/models outright (no grant fan-out); its enabled models
  // union into listProjectModels so the composer + ModelGate pick them up.
  const projectModelProviders = new Map<string, ModelProvider[]>();
  function seedModel(id: string, name: string, model: string): void {
    models.set(id, {
      id,
      name,
      base_url: `http://mockllm.jcloud.svc.cluster.local:8081/v1`,
      model_name: model,
      context_window: 0,
      capabilities: { reasoning: false, tools: true, image: false },
      api_key_set: true,
      created_at: nowISO(),
      updated_at: nowISO(),
      updated_by: 'demo-admin',
      granted_project_ids: [],
      granted_account_ids: [],
    });
    modelGrants.set(id, new Set());
    modelAccountGrants.set(id, new Set());
  }
  seedModel('mdl_gpt4o', 'GPT-4o (mock)', 'openai/gpt-4o');

  modelProviders.set('prv_openai', {
    id: 'prv_openai',
    name: 'OpenAI compatible',
    kind: 'openai',
    base_url: 'http://mockllm.jcloud.svc.cluster.local:8081/v1',
    auth_type: 'api_key',
    api_key_set: true,
    catalog_mode: 'auto',
    catalog_available: true,
    project_grants: 0,
    created_at: nowISO(),
    updated_at: nowISO(),
    updated_by: 'demo-admin',
    models: [{
      id: 'mdl_gpt4o',
      provider_id: 'prv_openai',
      name: 'GPT-4o (mock)',
      model_id: 'gpt-4o',
      runtime_model_name: 'openai/gpt-4o',
      context_window: 128_000,
      capabilities: { reasoning: true, tools: true, image: true },
      source: 'catalog',
      granted_project_ids: [],
      granted_account_ids: [],
    }],
  });

  /** Recompute authorization lists from the grant sets (view helper). */
  function modelView(m: Model): Model {
    return {
      ...m,
      granted_project_ids: [...(modelGrants.get(m.id) ?? [])].sort(),
      granted_account_ids: [...(modelAccountGrants.get(m.id) ?? [])].sort(),
    };
  }

  function providerView(provider: ModelProvider): ModelProvider {
    const modelsWithGrants = provider.models.map((model) => ({
      ...model,
      capabilities: { ...model.capabilities },
      granted_project_ids: [...(modelGrants.get(model.id) ?? [])].sort(),
      granted_account_ids: [...(modelAccountGrants.get(model.id) ?? [])].sort(),
    }));
    const grantedProjects = new Set(modelsWithGrants.flatMap((model) => model.granted_project_ids));
    return {
      ...provider,
      models: modelsWithGrants,
      project_grants: grantedProjects.size,
    };
  }
  function grantAllModelsTo(projectId: string): void {
    for (const set of modelGrants.values()) set.add(projectId);
  }

  /** Project provider view: project models carry `enabled` and NO grants. */
  function projectProviderView(provider: ModelProvider): ModelProvider {
    return {
      ...provider,
      models: provider.models.map((model) => ({
        ...model,
        capabilities: { ...model.capabilities },
      })),
    };
  }

  /** Validate + normalise a header map (mirrors the orchestrator's 400s). */
  function normalizeHeaders(headers: Record<string, string> | undefined): Record<string, string> | undefined {
    if (headers === undefined) return undefined;
    const out: Record<string, string> = {};
    for (const [key, value] of Object.entries(headers)) {
      if (!key.trim() || value.trim() === '') {
        throw badRequest('header name and value are required');
      }
      out[key.trim()] = value;
    }
    return out;
  }

  /** The project-owned providers store for one project. */
  function projectProvidersOf(projectId: string): ModelProvider[] {
    let list = projectModelProviders.get(projectId);
    if (!list) {
      list = [];
      projectModelProviders.set(projectId, list);
    }
    return list;
  }

  function findProjectProvider(projectId: string, id: string): ModelProvider {
    const provider = projectProvidersOf(projectId).find((p) => p.id === id);
    if (!provider) throw new ApiError(404, 'model provider not found');
    return provider;
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
      repo_html_url: mockRepoHTMLURL('gitea', ownerNamePath),
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
    async getSetupStatus(): Promise<SetupStatus> { return delay({ setup_required: false, public_url: 'http://localhost:5173', login_provider_count: 1 }); },
    async updateSetup(input: SetupInput): Promise<SetupStatus> { return delay({ setup_required: false, public_url: input.public_url, login_provider_count: 1 }); },
    async getClusterProviderConfig(provider: ProviderKind): Promise<ClusterProviderConfig> { return delay({ ...clusterProviders.get(provider)! }); },
    async updateClusterProviderConfig(provider: ProviderKind, input: UpdateClusterProviderConfigInput): Promise<ClusterProviderConfig> { const next = { ...clusterProviders.get(provider)!, ...input, configured: true, config_revision: (clusterProviders.get(provider)?.config_revision ?? 0) + 1 }; clusterProviders.set(provider, next); return delay({ ...next }); },
    async testClusterProviderConfig(provider: ProviderKind): Promise<ClusterProviderConfig> { const next = { ...clusterProviders.get(provider)!, health: 'healthy' as const, health_message: 'Connection verified' }; clusterProviders.set(provider, next); return delay({ ...next }); },
    async getClusterProviderImpact(): Promise<{ affected_installations: number; affected_projects: number }> { return delay({ affected_installations: 0, affected_projects: 0 }); },
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
    async resumeSession(runId: string, prompt: string, options?: ResumeSessionOptions): Promise<Run> {
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
      const permissionMode = options?.permission_mode === undefined
        ? (orig.permission_mode === 'approval' ? 'approval' : '')
        : options.permission_mode === 'approval' ? 'approval' : '';
      const svc = services.get(orig.project_id)?.find((service) => service.id === orig.service_id);
      if (!svc) throw new ApiError(409, 'the service for this session is unavailable');
      const modelId = resolveModelForRun(
        orig.project_id,
        svc,
        options?.model_id === undefined ? orig.model_id ?? undefined : options.model_id,
      );
      const run = makeRun(
        orig.project_id,
        orig.service_id,
        trimmed,
        undefined,
        1,
        true,
        permissionMode,
        orig.id,
        orig.acp_session_id,
      );
      run.model_id = modelId ?? undefined;
      run.model_name = modelId ? models.get(modelId)?.model_name : undefined;
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
        runner: {
          image: 'ghcr.io/jcloud/runner:demo',
          prewarm: {
            supported: true,
            desired: 2,
            ready: prewarmSyncedAt ? 2 : 0,
            image: 'ghcr.io/jcloud/runner:demo',
            last_sync: prewarmSyncedAt,
          },
        },
        namespace: 'jcloud',
        launcher: 'kubernetes',
        auth: {
          providers: ['gitea'],
          users_count: DEMO_USERS.length,
        },
        archive: {
          enabled: false,
          reason: 'S3_ARCHIVE_BUCKET and object-storage credentials are not configured',
        },
      };
      return delay(info);
    },

    async prewarmRunnerImage(): Promise<RunnerPrewarm> {
      prewarmSyncedAt = nowISO();
      return delay({
        supported: true,
        desired: 2,
        ready: 2,
        image: 'ghcr.io/jcloud/runner:demo',
        last_sync: prewarmSyncedAt,
      });
    },

    /* ---- model providers + discovery ------------------------------------ */
    async listModelProviders(): Promise<ModelProvider[]> {
      return delay([...modelProviders.values()].map(providerView));
    },

    async createModelProvider(input: CreateModelProviderInput): Promise<ModelProvider> {
      const name = input.name?.trim() ?? '';
      const kind = input.kind?.trim().toLowerCase() ?? '';
      const baseUrl = input.base_url?.trim().replace(/\/$/, '') ?? '';
      if (!name) throw badRequest('name is required');
      if (!/^[a-z0-9_-]+$/.test(kind)) throw badRequest('kind must be a lowercase provider id');
      if (!/^https?:\/\/.+/i.test(baseUrl)) throw badRequest('base_url must be an http(s) URL');
      if ([...modelProviders.values()].some((provider) => provider.name === name)) {
        throw new ApiError(409, `a model provider named '${name}' already exists`, {
          error: { code: 'conflict', message: `provider '${name}' exists` },
        });
      }
      const id = genId('prv');
      const provider: ModelProvider = {
        id,
        name,
        kind,
        base_url: baseUrl,
        auth_type: input.auth_type,
        api_key_set: input.auth_type === 'api_key' && !!input.api_key,
        catalog_mode: input.catalog_mode,
        catalog_available: input.catalog_mode === 'disabled' ? false : null,
        models: [],
        project_grants: 0,
        created_at: nowISO(),
        updated_at: nowISO(),
        updated_by: 'demo-admin',
      };
      modelProviders.set(id, provider);
      return delay(providerView(provider));
    },

    async updateModelProvider(id: string, input: UpdateModelProviderInput): Promise<ModelProvider> {
      const provider = modelProviders.get(id);
      if (!provider) throw new ApiError(404, 'model provider not found');
      if (input.name !== undefined) provider.name = input.name.trim();
      if (input.kind !== undefined) provider.kind = input.kind.trim().toLowerCase();
      if (input.base_url !== undefined) provider.base_url = input.base_url.trim().replace(/\/$/, '');
      if (input.auth_type !== undefined) {
        provider.auth_type = input.auth_type;
        if (input.auth_type !== 'api_key') provider.api_key_set = false;
      }
      if (input.api_key !== undefined) provider.api_key_set = input.api_key !== '';
      if (input.catalog_mode !== undefined) {
        provider.catalog_mode = input.catalog_mode;
        provider.catalog_available = input.catalog_mode === 'disabled' ? false : null;
      }
      provider.updated_at = nowISO();
      for (const providerModel of provider.models) {
        const legacy = models.get(providerModel.id);
        if (legacy) {
          legacy.base_url = provider.base_url;
          legacy.model_name = `${provider.kind}/${providerModel.model_id}`;
        }
      }
      return delay(providerView(provider));
    },

    async deleteModelProvider(id: string): Promise<void> {
      const provider = modelProviders.get(id);
      if (!provider) throw new ApiError(404, 'model provider not found');
      for (const model of provider.models) {
        models.delete(model.id);
        modelGrants.delete(model.id);
        modelAccountGrants.delete(model.id);
      }
      modelProviders.delete(id);
      return delay(undefined);
    },

    async verifyModelProvider(id: string): Promise<ModelProviderVerification> {
      const provider = modelProviders.get(id);
      if (!provider) throw new ApiError(404, 'model provider not found');
      provider.catalog_available = provider.catalog_mode !== 'disabled';
      provider.last_verified_at = nowISO();
      provider.last_verification_error = '';
      provider.updated_at = nowISO();
      return delay({
        reachable: true,
        catalog_available: provider.catalog_available,
        latency_ms: 42,
      });
    },

    async getModelProviderCatalog(id: string): Promise<CatalogModel[]> {
      const provider = modelProviders.get(id);
      if (!provider) throw new ApiError(404, 'model provider not found');
      if (provider.catalog_mode === 'disabled') {
        throw new ApiError(409, 'this provider does not expose a model catalog; add a custom model', {
          error: { code: 'catalog_unavailable', message: 'this provider does not expose a model catalog; add a custom model' },
        });
      }
      return delay([
        { id: 'gpt-4o', name: 'GPT-4o', context_window: 128_000, capabilities: { reasoning: true, tools: true, image: true } },
        { id: 'o3', name: 'o3', context_window: 200_000, capabilities: { reasoning: true, tools: true, image: true } },
      ]);
    },

    async createProviderModel(providerId: string, input: CreateProviderModelInput): Promise<ProviderModel> {
      const provider = modelProviders.get(providerId);
      if (!provider) throw new ApiError(404, 'model provider not found');
      const name = input.name?.trim() ?? '';
      const modelId = input.model_id?.trim() ?? '';
      if (!name) throw badRequest('name is required');
      if (!modelId || /\s/.test(modelId)) throw badRequest('model_id is required and cannot contain whitespace');
      if (provider.models.some((model) => model.model_id === modelId)) {
        throw new ApiError(409, 'that model is already configured');
      }
      const id = genId('mdl');
      const providerModel: ProviderModel = {
        id,
        provider_id: providerId,
        name,
        model_id: modelId,
        runtime_model_name: `${provider.kind}/${modelId}`,
        context_window: input.context_window,
        capabilities: { ...input.capabilities },
        source: input.source,
        granted_project_ids: [],
        granted_account_ids: [],
      };
      provider.models.push(providerModel);
      provider.updated_at = nowISO();
      models.set(id, {
        id,
        name,
        base_url: provider.base_url,
        model_name: providerModel.runtime_model_name,
        context_window: providerModel.context_window,
        capabilities: { ...providerModel.capabilities },
        api_key_set: provider.api_key_set,
        created_at: nowISO(),
        updated_at: nowISO(),
        updated_by: 'demo-admin',
        granted_project_ids: [],
        granted_account_ids: [],
      });
      modelGrants.set(id, new Set());
      modelAccountGrants.set(id, new Set());
      return delay({ ...providerModel, capabilities: { ...providerModel.capabilities } });
    },

    /* ---- project-owned model providers (M2) ------------------------------- */
    async listProjectModelProviders(projectId: string): Promise<ModelProvider[]> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      return delay(projectProvidersOf(projectId).map(projectProviderView));
    },

    async createProjectModelProvider(projectId: string, input: CreateModelProviderInput): Promise<ModelProvider> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      const name = input.name?.trim() ?? '';
      const kind = input.kind?.trim().toLowerCase() ?? '';
      const baseUrl = input.base_url?.trim().replace(/\/$/, '') ?? '';
      if (!name) throw badRequest('name is required');
      if (!/^[a-z0-9_-]+$/.test(kind)) throw badRequest('kind must be a lowercase provider id');
      if (!/^https?:\/\/.+/i.test(baseUrl)) throw badRequest('base_url must be an http(s) URL');
      if (input.api_key && input.auth_type !== 'api_key') {
        throw badRequest('api_key is only valid when auth_type is api_key');
      }
      const list = projectProvidersOf(projectId);
      if (list.some((p) => p.name === name)) {
        throw new ApiError(409, `a model provider named '${name}' already exists`, {
          error: { code: 'conflict', message: `provider '${name}' exists` },
        });
      }
      const headers = normalizeHeaders(input.headers);
      const provider: ModelProvider = {
        id: genId('prv'),
        project_id: projectId,
        name,
        kind,
        base_url: baseUrl,
        auth_type: input.auth_type,
        api_key_set: input.auth_type === 'api_key' && !!input.api_key,
        headers_set: !!(headers && Object.keys(headers).length > 0),
        catalog_mode: input.catalog_mode,
        catalog_available: input.catalog_mode === 'disabled' ? false : null,
        models: [],
        created_at: nowISO(),
        updated_at: nowISO(),
        updated_by: 'demo-admin',
      };
      list.push(provider);
      return delay(projectProviderView(provider));
    },

    async updateProjectModelProvider(projectId: string, id: string, input: UpdateModelProviderInput): Promise<ModelProvider> {
      const provider = findProjectProvider(projectId, id);
      if (input.name !== undefined) provider.name = input.name.trim();
      if (input.kind !== undefined) provider.kind = input.kind.trim().toLowerCase();
      if (input.base_url !== undefined) provider.base_url = input.base_url.trim().replace(/\/$/, '');
      if (input.auth_type !== undefined) {
        provider.auth_type = input.auth_type;
        if (input.auth_type !== 'api_key') provider.api_key_set = false;
      }
      if (input.api_key !== undefined) provider.api_key_set = input.api_key !== '';
      if (input.headers !== undefined) {
        const headers = normalizeHeaders(input.headers);
        provider.headers_set = !!(headers && Object.keys(headers).length > 0);
      }
      if (input.catalog_mode !== undefined) {
        provider.catalog_mode = input.catalog_mode;
        provider.catalog_available = input.catalog_mode === 'disabled' ? false : null;
      }
      // Any probe-affecting change resets the verification state (mirrors M1).
      provider.last_verified_at = undefined;
      provider.last_verification_error = undefined;
      provider.updated_at = nowISO();
      for (const m of provider.models) m.runtime_model_name = `${provider.kind}/${m.model_id}`;
      return delay(projectProviderView(provider));
    },

    async deleteProjectModelProvider(projectId: string, id: string): Promise<void> {
      findProjectProvider(projectId, id);
      projectModelProviders.set(projectId, projectProvidersOf(projectId).filter((p) => p.id !== id));
      return delay(undefined);
    },

    async verifyProjectModelProvider(projectId: string, id: string): Promise<ModelProviderVerification> {
      const provider = findProjectProvider(projectId, id);
      provider.catalog_available = provider.catalog_mode !== 'disabled';
      provider.last_verified_at = nowISO();
      provider.last_verification_error = undefined;
      provider.updated_at = nowISO();
      return delay({ reachable: true, catalog_available: provider.catalog_available, latency_ms: 37 });
    },

    async getProjectModelProviderCatalog(projectId: string, id: string): Promise<CatalogModel[]> {
      const provider = findProjectProvider(projectId, id);
      if (provider.catalog_mode === 'disabled') {
        throw new ApiError(409, 'this provider does not expose a model catalog; add a custom model', {
          error: { code: 'catalog_unavailable', message: 'this provider does not expose a model catalog; add a custom model' },
        });
      }
      return delay([
        { id: 'gpt-4o', name: 'GPT-4o', context_window: 128_000, capabilities: { reasoning: true, tools: true, image: true } },
        { id: 'o3', name: 'o3', context_window: 200_000, capabilities: { reasoning: true, tools: true, image: true } },
      ]);
    },

    async createProjectProviderModel(projectId: string, id: string, input: CreateProviderModelInput): Promise<ProviderModel> {
      const provider = findProjectProvider(projectId, id);
      const name = input.name?.trim() ?? '';
      const modelId = input.model_id?.trim() ?? '';
      if (!name) throw badRequest('name is required');
      if (!modelId || /\s/.test(modelId)) throw badRequest('model_id is required and cannot contain whitespace');
      if (provider.models.some((m) => m.model_id === modelId)) {
        throw new ApiError(409, 'that model is already configured');
      }
      const model: ProviderModel = {
        id: genId('mdl'),
        provider_id: provider.id,
        name,
        model_id: modelId,
        runtime_model_name: `${provider.kind}/${modelId}`,
        context_window: input.context_window,
        capabilities: { ...input.capabilities },
        source: input.source,
        enabled: true,
      };
      provider.models.push(model);
      provider.updated_at = nowISO();
      return delay({ ...model, capabilities: { ...model.capabilities } });
    },

    async updateProjectProviderModel(projectId: string, providerId: string, modelId: string, input: UpdateProviderModelInput): Promise<ProviderModel> {
      const provider = findProjectProvider(projectId, providerId);
      const model = provider.models.find((m) => m.id === modelId);
      if (!model) throw new ApiError(404, 'model not found');
      if (input.name !== undefined) {
        const name = input.name.trim();
        if (!name) throw badRequest('name cannot be empty');
        model.name = name;
      }
      if (input.context_window !== undefined) model.context_window = input.context_window;
      if (input.capabilities !== undefined) model.capabilities = { ...input.capabilities };
      if (input.enabled !== undefined) model.enabled = input.enabled;
      provider.updated_at = nowISO();
      return delay({ ...model, capabilities: { ...model.capabilities } });
    },

    async deleteProjectProviderModel(projectId: string, providerId: string, modelId: string): Promise<void> {
      const provider = findProjectProvider(projectId, providerId);
      if (!provider.models.some((m) => m.id === modelId)) throw new ApiError(404, 'model not found');
      provider.models = provider.models.filter((m) => m.id !== modelId);
      provider.updated_at = nowISO();
      return delay(undefined);
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
        context_window: 0, capabilities: { reasoning: false, tools: false, image: false },
        created_at: nowISO(), updated_at: nowISO(), updated_by: 'demo-admin',
        granted_project_ids: [], granted_account_ids: [],
      };
      models.set(id, m);
      modelGrants.set(id, new Set());
      modelAccountGrants.set(id, new Set());
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
      if (input.context_window !== undefined) m.context_window = input.context_window;
      if (input.capabilities !== undefined) m.capabilities = { ...input.capabilities };
      if (input.api_key !== undefined) m.api_key_set = input.api_key !== '';
      m.updated_at = nowISO();
      models.set(id, m);
      return delay(modelView(m));
    },

    async deleteModel(id: string): Promise<void> {
      if (!models.delete(id)) throw new ApiError(404, 'model not found');
      modelGrants.delete(id);
      modelAccountGrants.delete(id);
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

    async grantModelToAccount(modelId: string, userId: string): Promise<Model> {
      const m = models.get(modelId);
      if (!m || !DEMO_USERS.some((user) => user.id === userId)) throw new ApiError(404, 'model or account not found');
      (modelAccountGrants.get(modelId) ?? new Set()).add(userId);
      return delay(modelView(m));
    },

    async revokeModelFromAccount(modelId: string, userId: string): Promise<Model> {
      const m = models.get(modelId);
      if (!m) throw new ApiError(404, 'model not found');
      modelAccountGrants.get(modelId)?.delete(userId);
      return delay(modelView(m));
    },

    async listProjectModels(projectId: string): Promise<ProjectModels> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      // M1 union: project-owned ENABLED models ∪ cluster-granted models.
      const owned = projectProvidersOf(projectId)
        .flatMap((p) => p.models)
        .filter((m) => m.enabled !== false)
        .map((m) => ({
          id: m.id, name: m.name, model_name: m.runtime_model_name,
          capabilities: m.capabilities,
        }));
      const granted = [...models.values()]
        .filter((m) => modelGrants.get(m.id)?.has(projectId))
        .map((m) => ({
          id: m.id, name: m.name, model_name: m.model_name,
          // Legacy cluster-catalog rows predate explicit capability metadata.
          capabilities: { reasoning: false, tools: true, image: false },
        }));
      return delay({ models: [...owned, ...granted], env_fallback: false });
    },

    /* ---- kanban board embed (D31) ----------------------------------------- */
    // The member+ reduced link list is derived from current Service Kanban
    // Automations. It has no legacy credential/link state.
    async listProjectBoardLinks(projectId: string): Promise<BoardEmbedLink[]> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      const serviceIds = new Set((services.get(projectId) ?? []).map((service) => service.id));
      return delay(
        [...projectAutomations.values()]
          .filter((spec) => spec.automation.trigger_kind === 'kanban' && serviceIds.has(spec.automation.service_id) && !!spec.kanban)
          .map((spec) => {
            const installation = [...pluginList(projectId).values()].find(
              (plugin) => plugin.id === spec.kanban!.installation_id,
            );
            const workspaceId = installation?.workspace_id ?? 'ws_team';
            const board = (embeddedBoards[workspaceId] ?? []).find(
              (candidate) => candidate.ref === spec.kanban!.board_ref || candidate.id === spec.kanban!.board_ref,
            );
            return {
              id: spec.automation.id,
              workspace_id: workspaceId,
              board_ref: board?.id ?? spec.kanban!.board_ref,
              board_title: board?.title,
              service_id: spec.automation.service_id,
              trigger_column: spec.kanban!.trigger_column,
              done_column: spec.kanban!.done_column,
              enabled: spec.automation.enabled,
            } satisfies BoardEmbedLink;
          }),
      );
    },
    async boardListDocuments(
      projectId: string,
      workspaceId: string,
    ): Promise<JTypeDocumentListItem[]> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      const boards = embeddedBoards[workspaceId] ?? [];
      return delay(
        boards.map((b) => ({
          id: `doc_${b.id}`,
          relativePath: b.ref,
          title: b.title,
          isPublished: true,
          contentHash: `h_${b.id}`,
          updatedClock: 1,
          versionId: 'v1',
        })),
      );
    },
    async boardGetDocument(
      projectId: string,
      workspaceId: string,
      docId: string,
    ): Promise<JTypeCloudDocument> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      const board = (embeddedBoards[workspaceId] ?? []).find(
        (b) => `doc_${b.id}` === docId,
      );
      if (!board) {
        throw new ApiError(404, `no document '${docId}'`, {
          error: { code: 'document_not_found', message: `no document '${docId}'` },
        });
      }
      return delay({
        relativePath: board.ref,
        title: board.title,
        isPublished: true,
        content: JSON.stringify({
          id: board.id,
          title: board.title,
          columns: board.columns.map((c) => ({ key: c.key, name: c.name })),
        }),
        contentHash: `h_${board.id}`,
        versionId: 'v1',
        updatedClock: 1,
      });
    },
    async boardSaveDocument(
      projectId: string,
      _workspaceId: string,
      req: JTypeSaveDocumentRequest,
    ): Promise<JTypeSaveDocumentResponse> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      return delay({
        relativePath: req.relativePath,
        contentHash: `h_${Math.random().toString(36).slice(2, 8)}`,
        updatedClock: 2,
        mergeStatus: 'accepted',
      });
    },

    async listProjectPlugins(projectId: string): Promise<ProjectPlugin[]> {
      return delay([...pluginList(projectId).values()].map(copyPlugin));
    },
    async startPluginInstall(projectId: string, provider: ProviderKind, input: PluginConsentInput): Promise<PluginInstallStart> {
      if (!input.consent_accepted || !input.consent_version) throw badRequest('plugin consent is required');
      const plugin = pluginList(projectId).get(provider)!;
      plugin.id = plugin.id ?? genId('plugin');
      plugin.project_id = projectId;
      plugin.status = 'enabled';
      plugin.external_account = provider === 'jtype' ? 'JType workspace' : `${provider}-account`;
      plugin.external_account_id = `${provider}-demo-id`;
      plugin.scopes = [...input.scopes];
      plugin.token_set = true;
      plugin.consent_version = input.consent_version;
      plugin.consented_at = nowISO();
      if (provider === 'jtype') {
        plugin.workspace_id = 'ws_team';
      }
      plugin.installed_at = nowISO();
      plugin.installed_by = 'Demo owner';
      plugin.capabilities = { supported_actions: ['push.updated', 'pull_request.opened', 'pull_request.synchronized', 'comment.created', 'issue.opened', 'check.completed', 'release.published'] };
      pluginList(projectId).set(provider, plugin);
      if (provider === 'jtype') {
        return delay({
          connect_id: 'connect-jtype-demo',
          user_code: 'JTYP-42',
          verification_uri: 'https://jtype.example/device',
          verification_uri_complete: 'https://jtype.example/device?user_code=JTYP-42',
          expires_in: 600,
          interval: 2,
        });
      }
      return delay({
        authorize_url: `/auth/plugins/${provider}?project_id=${encodeURIComponent(projectId)}`,
        plugin: copyPlugin(plugin),
      });
    },
    async listGitHubAppInstallations() {
      return delay([{ id: '12345', account_id: '99', account: 'acme', target_type: 'Organization', repository_selection: 'selected' }]);
    },
    async previewGitHubAppInstallationConsent(_projectId: string, installationId: string) {
      return delay({
        installation_id: installationId,
        account: 'acme',
        scopes: ['actions:write', 'checks:write', 'contents:write', 'issues:write', 'metadata:read', 'pull_requests:write', 'repository_selection:selected'],
        repository_selection: 'selected',
        scope_digest: `mock-${installationId}`,
      });
    },
    async selectGitHubAppInstallation(projectId: string, installationId: string, input: PluginConsentInput): Promise<ProjectPlugin> {
      if (!installationId) throw badRequest('GitHub App Installation is required');
      const plugin = pluginList(projectId).get('github')!;
      Object.assign(plugin, {
        id: genId('plugin'), project_id: projectId, status: 'enabled',
        external_account_id: '99', external_account: 'acme',
        scopes: ['contents:write'], token_set: false, consent_version: input.consent_version,
        consented_at: nowISO(),
      });
      return delay(copyPlugin(plugin));
    },
    async getJTypePluginConnectStatus() {
      return delay({ status: 'complete' as const, token_set: true, token_expires_at: nowISO(3_600_000) });
    },
    async setProjectPluginEnabled(installationId: string, enabled: boolean): Promise<ProjectPlugin> {
      for (const list of projectPlugins.values()) {
        const plugin = [...list.values()].find((item) => item.id === installationId);
        if (!plugin) continue;
        plugin.status = enabled ? 'enabled' : 'disabled';
        return delay(copyPlugin(plugin));
      }
      throw new ApiError(404, 'plugin installation not found');
    },
    async setProjectPluginWorkspace(installationId: string, workspaceId: string): Promise<ProjectPlugin> {
      for (const list of projectPlugins.values()) {
        const plugin = [...list.values()].find((item) => item.id === installationId);
        if (!plugin) continue;
        if (plugin.provider !== 'jtype') throw badRequest('workspace is only valid for JType');
        plugin.workspace_id = workspaceId;
        plugin.status = 'enabled';
        return delay(copyPlugin(plugin));
      }
      throw new ApiError(404, 'plugin installation not found');
    },
    async getProjectPluginImpact(projectId: string, installationId: string): Promise<{ services: number; automations: number }> {
      const plugin = [...pluginList(projectId).values()].find((item) => item.id === installationId);
      if (!plugin) throw new ApiError(404, 'plugin installation not found');
      const dependentServices = (services.get(projectId) ?? []).filter((service) => service.provider === plugin.provider);
      const dependentServiceIds = new Set(dependentServices.map((service) => service.id));
      return delay({
        services: dependentServices.length,
        automations: [...projectAutomations.values()].filter((spec) => dependentServiceIds.has(spec.automation.service_id)).length,
      });
    },
    async listProjectPluginAudit(_projectId: string, installationId: string) {
      return delay([{
        id: genId('audit'),
        project_id: _projectId,
        installation_id: installationId,
        event_type: 'connected',
        created_at: nowISO(),
      }]);
    },
    async uninstallProjectPlugin(installationId: string, _force = false): Promise<void> {
      for (const list of projectPlugins.values()) {
        const plugin = [...list.values()].find((item) => item.id === installationId);
        if (!plugin) continue;
        Object.assign(plugin, {
          id: undefined, project_id: undefined, status: 'not_connected',
          external_account: undefined, external_account_id: undefined, workspace_id: undefined,
          scopes: [], service_count: 0, automation_count: 0, installed_at: undefined,
          installed_by: undefined, token_set: false,
        });
        return delay(undefined);
      }
      throw new ApiError(404, 'plugin installation not found');
    },
    async listPluginRepositories(projectId: string, installationId: string, q?: string): Promise<PluginRepositoryResource[]> {
      const plugin = [...pluginList(projectId).values()].find((item) => item.id === installationId);
      if (!plugin) throw new ApiError(404, 'plugin installation not found');
      const rows = [
        { id: 'repo-1', full_name: 'acme/demo', clone_url: 'https://example.test/acme/demo.git', html_url: 'https://example.test/acme/demo', default_branch: 'main', private: false },
        { id: 'repo-2', full_name: 'acme/platform', clone_url: 'https://example.test/acme/platform.git', html_url: 'https://example.test/acme/platform', default_branch: 'main', private: true },
      ];
      const needle = q?.trim().toLowerCase();
      return delay(needle ? rows.filter((row) => row.full_name.includes(needle)) : rows);
    },
    async listPluginWorkspaces(projectId: string, installationId: string): Promise<PluginWorkspaceResource[]> {
      const plugin = [...pluginList(projectId).values()].find((item) => item.id === installationId);
      if (!plugin) throw new ApiError(404, 'plugin installation not found');
      return delay([{ id: plugin.workspace_id ?? 'ws_team', name: 'My Team' }]);
    },
    async listPluginBoards(projectId: string, installationId: string, workspaceId?: string): Promise<PluginBoardResource[]> {
      const plugin = [...pluginList(projectId).values()].find((item) => item.id === installationId);
      if (!plugin) throw new ApiError(404, 'plugin installation not found');
      if (!workspaceId) return delay([]);
      return delay([{ id: 'board-1', ref: 'team.delivery', title: 'Delivery', columns: [
        { key: 'todo', name: 'Todo' }, { key: 'ai', name: 'AI' }, { key: 'done', name: 'Done' },
      ] }]);
    },
    async getProviderCapabilities(provider: ProviderKind) {
      const actions = provider === 'github' ? [
        ['push', ['updated']],
        ['pull_request', ['opened', 'reopened', 'synchronized', 'ready', 'closed', 'merged']],
        ['review', ['approved', 'changes_requested', 'commented', 'dismissed', 'approval_removed']],
        ['comment', ['created']], ['issue', ['opened', 'reopened', 'updated', 'closed']],
        ['check', ['completed']], ['tag', ['created', 'deleted']], ['release', ['published', 'updated', 'deleted']],
      ] : provider === 'gitlab' ? [
        ['push', ['updated']], ['pull_request', ['opened', 'reopened', 'synchronized', 'closed', 'merged']],
        ['review', ['approved', 'approval_removed', 'commented']], ['comment', ['created']],
        ['issue', ['opened', 'reopened', 'updated', 'closed']], ['check', ['completed']],
        ['tag', ['created', 'deleted']], ['release', ['published', 'updated', 'deleted']],
      ] : provider === 'gitea' ? [
        ['push', ['updated']], ['pull_request', ['opened', 'reopened', 'synchronized', 'closed', 'merged']],
        ['review', ['approved', 'changes_requested', 'commented']], ['comment', ['created']],
        ['issue', ['opened', 'reopened', 'updated', 'closed']], ['check', ['completed']],
        ['tag', ['created', 'deleted']], ['release', ['published', 'updated', 'deleted']],
      ] : [];
      return delay({
        provider,
        minimum_version: provider === 'gitlab' ? '17.11' : provider === 'gitea' ? '1.25' : undefined,
        capabilities: actions.map(([family, supported]) => ({
          family: family as string,
          actions: supported as string[],
        })),
      });
    },
    async listProjectAutomations(projectId: string): Promise<ProjectAutomationSpec[]> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      const serviceIds = new Set((services.get(projectId) ?? []).map((service) => service.id));
      return delay([...projectAutomations.values()].filter((spec) => serviceIds.has(spec.automation.service_id)));
    },
    async getProjectAutomation(projectId: string, automationId: string): Promise<ProjectAutomationSpec> {
      const spec = projectAutomations.get(automationId);
      const serviceIds = new Set((services.get(projectId) ?? []).map((service) => service.id));
      if (!spec || !serviceIds.has(spec.automation.service_id)) throw new ApiError(404, 'automation not found');
      return delay(structuredClone(spec));
    },
    async createProjectAutomation(projectId: string, input: CreateProjectAutomationInput): Promise<ProjectAutomationSpec> {
      if (!projects.has(projectId)) throw new ApiError(404, 'project not found');
      if (!input.name.trim() || !input.prompt_template.trim()) throw badRequest('name and prompt_template are required');
      const spec = automationFromInput(input);
      projectAutomations.set(spec.automation.id, spec);
      return delay(structuredClone(spec));
    },
    async updateProjectAutomation(projectId: string, automationId: string, input: UpdateProjectAutomationInput): Promise<ProjectAutomationSpec> {
      const existing = projectAutomations.get(automationId);
      const serviceIds = new Set((services.get(projectId) ?? []).map((service) => service.id));
      if (!existing || !serviceIds.has(existing.automation.service_id)) throw new ApiError(404, 'automation not found');
      const merged: CreateProjectAutomationInput = {
        service_id: input.service_id ?? existing.automation.service_id,
        name: input.name ?? existing.automation.name,
        prompt_template: input.prompt_template ?? existing.automation.prompt_template,
        enabled: input.enabled ?? existing.automation.enabled,
        ignore_jcode: input.ignore_jcode ?? existing.automation.ignore_jcode,
        scm: input.scm ?? (existing.scm ? { ...existing.scm, actions: existing.actions ?? [] } : undefined),
        kanban: input.kanban ?? existing.kanban,
        cron: input.cron ?? existing.cron,
      };
      const updated = automationFromInput(merged, existing);
      projectAutomations.set(automationId, updated);
      return delay(structuredClone(updated));
    },
    async deleteProjectAutomation(projectId: string, automationId: string): Promise<void> {
      const existing = projectAutomations.get(automationId);
      const serviceIds = new Set((services.get(projectId) ?? []).map((service) => service.id));
      if (!existing || !serviceIds.has(existing.automation.service_id)) throw new ApiError(404, 'automation not found');
      projectAutomations.delete(automationId);
      return delay(undefined);
    },
    async getServiceKanban(serviceId: string): Promise<ProjectAutomationSpec> {
      const spec = [...projectAutomations.values()].find((item) =>
        item.automation.service_id === serviceId && item.automation.trigger_kind === 'kanban');
      if (!spec) throw new ApiError(404, 'Kanban is not enabled');
      return delay(structuredClone(spec));
    },
    async putServiceKanban(serviceId, input): Promise<ProjectAutomationSpec> {
      const projectEntry = [...services.entries()].find(([, list]) => list.some((service) => service.id === serviceId));
      if (!projectEntry) throw new ApiError(404, 'service not found');
      const existing = [...projectAutomations.values()].find((item) =>
        item.automation.service_id === serviceId && item.automation.trigger_kind === 'kanban');
      const spec = automationFromInput({
        service_id: serviceId,
        name: 'Kanban',
        prompt_template: 'Complete the task described by the JType card.',
        enabled: input.enabled ?? true,
        ignore_jcode: true,
        kanban: {
          installation_id: input.installation_id,
          board_ref: input.board_ref,
          trigger_column: 'ai',
          done_column: 'done',
        },
      }, existing);
      projectAutomations.set(spec.automation.id, spec);
      return delay(structuredClone(spec));
    },
    async deleteServiceKanban(serviceId: string): Promise<void> {
      const existing = [...projectAutomations.values()].find((item) =>
        item.automation.service_id === serviceId && item.automation.trigger_kind === 'kanban');
      if (existing) projectAutomations.delete(existing.automation.id);
      return delay(undefined);
    },

    /* ---- integrations (D19 / F5) ------------------------------------------ */

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
      const boundProvider = prov;
      const svc: Service = {
        id: genId('svc'),
        project_id: projectId,
        name,
        repo_kind: boundProvider ? 'provider' : 'raw',
        provider: boundProvider ?? undefined,
        repo_owner_name: boundProvider
          ? input.owner_name?.trim() || ownerName(repoUrl)
          : undefined,
        repo_html_url: boundProvider
          ? mockRepoHTMLURL(
              boundProvider,
              input.owner_name?.trim() || ownerName(repoUrl),
              repoUrl,
            )
          : undefined,
        raw_repo_url: boundProvider ? undefined : repoUrl,
        default_branch: input.default_branch?.trim() || 'main',
        git_mode: gitMode,
        integration_id: null,
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

    async deleteService(serviceId: string): Promise<void> {
      for (const [projectId, list] of services) {
        const index = list.findIndex((service) => service.id === serviceId);
        if (index < 0) continue;
        for (const [runId, run] of runs) {
          if (run.service_id === serviceId) runs.delete(runId);
        }
        list.splice(index, 1);
        services.set(projectId, list);
        const project = projects.get(projectId);
        if (project) project.services = [...list];
        return delay(undefined);
      }
      throw new ApiError(404, 'service not found');
    },

    async ensureServiceWebhook(serviceId: string) {
      for (const list of services.values()) {
        const svc = list.find((entry) => entry.id === serviceId);
        if (!svc) continue;
        if (svc.repo_kind !== 'provider' || !svc.provider) {
          throw new ApiError(409, 'This service is not a provider-backed repository, so it cannot receive PR review webhooks.', {
            error: { code: 'provider_webhook_unavailable', message: 'provider-backed repository required' },
          });
        }
        // This client is the explicitly-labelled demo rig; production performs
        // the real OAuth-only provider call in the orchestrator.
        return delay({
          provider: svc.provider,
          endpoint: `https://demo.j-code.net/webhooks/${svc.provider}`,
          status: 'synced' as const,
        });
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

    async listServiceBranches(serviceId: string): Promise<ServiceBranch[]> {
		let service: Service | undefined;
		for (const list of services.values()) {
			service = list.find((candidate) => candidate.id === serviceId);
			if (service) break;
		}
		if (!service) throw new ApiError(404, 'service not found');
		const defaultBranch = service.default_branch || 'main';
		return delay([
			{ name: defaultBranch, default: true, protected: true },
			{ name: 'develop', default: false },
			{ name: 'feature/demo', default: false },
		]);
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
      run.model_name = modelId ? models.get(modelId)?.model_name : undefined;
      return delay(publicRun(run));
    },

    async uploadRunAttachment(serviceId: string, file: File) {
      const service = [...services.values()].flat().find((candidate) => candidate.id === serviceId);
      if (!service) throw new ApiError(404, 'service not found');
      const now = new Date();
      const expires = new Date(now.getTime() + 10 * 60_000);
      return {
        stage: {
          id: genId('att'),
          project_id: service.project_id,
          display_name: file.name,
          content_type: file.type || 'application/octet-stream',
          size_bytes: file.size,
          created_at: now.toISOString(),
          expires_at: expires.toISOString(),
        },
        upload_url: '/api/v1/demo/attachment',
        expires_at: expires.toISOString(),
      };
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
