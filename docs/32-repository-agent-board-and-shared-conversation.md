# 32 · Repository Agent Board and shared Conversation

Status: approved product contract / implementation complete, deployment pending
Date: 2026-08-25

## Outcome

The personal Cloud product exposes Repository as the only workspace target. A
hidden singleton Project may remain as the tenancy, policy, and concurrency
boundary, and the existing Service row may remain as the first storage
implementation of a Repository, but neither name is part of the public product
or Repository execution contract. Temporary Project administration endpoints
remain an internal migration surface for the Console while the hidden personal
container still owns provider connections and concurrency policy; the removed
Service endpoints have no compatibility alias.

The new-task entry is an Account composer, not a Repository detail page and
not a connection wizard. Its Repository picker is populated directly from all
repositories visible to the Account's linked Git identities. Starting a task
against a repository for the first time materializes the hidden Project/Service
execution records and creates the Run in the same request; there is no prior
"connect Repository" requirement or visible association state.

Selecting a repository renders its complete Workspace immediately, even before
those internal records exist. Tasks, Board, Reviews, Automations, Usage, and
Settings use truthful empty states or documented defaults; first execution is
only a persistence boundary and never a UI availability boundary. Read-only
defaults do not fabricate successful API data. Task creation remains the
current materialization operation; controls whose write APIs require a durable
Repository id stay read-only until that prerequisite exists.

Each Repository can connect at most one existing JType Board. The resulting
Repository Agent Workflow has two equivalent Trigger producers:

1. moving a Card into the configured Agent queue column; and
2. pressing **Run with agent** on that Card.

Both producers create the same durable occurrence and enter the same readiness,
Run, Delivery, receipt, and writeback path. There is no private direct-Run path
for the button.

Conversation is a shared product surface. `jcode-ui-core` owns the runtime and
event types, `jcode-ui` owns rendering and interaction, and Desktop and Cloud
provide transport adapters. Cloud must not fork message, Markdown, tool,
approval, pending, or conversation-scroll rendering.

## Product language

- **Repository** — the user-selected source repository and execution target.
- **Agent Board** — the one JType Board connected to a Repository.
- **Repository Agent Workflow** — the durable policy that turns eligible Card
  actions into Runs.
- **Occurrence** — one idempotent request to execute one Card.
- **Conversation** — the ordered Run/session timeline rendered by shared
  `jcode-ui` surfaces.

Project and Service are internal migration terms only. New API responses, route
names, UI copy, telemetry labels, and user-facing errors use Repository.

## Locked decisions

1. Repository cardinality is `Repository 0..1 Agent Board`; a Board can be
   connected to only one Repository.
2. Only an existing JType Board can be connected. Cloud does not silently
   create a Board.
3. Trigger and work columns are required and distinct. Done is optional and, if
   present, distinct from both.
4. A successful `no_changes` result may move to Done. Completion is determined
   by the structured Run result plus successful Delivery/writeback, not by diff
   presence.
5. The Repository owner is the default `execution_account_id`. This is an
   authorization principal and remains separate from provenance/accountability.
6. A Repository Agent Workflow stores an explicit model and effort. It never
   inherits the mutable composer “last used model”.
7. Repository `AGENTS.md` is the only Repository Agent instruction source. The
   product does not add a second online instructions field.
8. Agent Board uses the built-in Developer Agent Profile and Cloud execution in
   the first release. Remote execution remains an explicit future target and
   never silently falls back to Cloud.
9. Existing global/singleton-Project concurrency limits remain authoritative.
10. Code reviews remain a separate entry and use the Reviewer Workflow.
11. The old Service HTTP API is removed rather than kept as a compatibility
    alias. Console and orchestrator cut over atomically.
12. Any repository visible to a linked Account Git identity can be selected in
    the Account composer. First execution lazily creates its internal
    Repository record; Repository detail is optional and never a prerequisite
    for conversation or task creation.
13. Welcome and Repository workspace are one Work Home. Successful OAuth or
    token login enters the composer directly; there is no intermediate welcome
    or landing card.
14. Remote is a composer context, not a permanent management destination. A
    dedicated Remote onboarding route remains for device login and pairing;
    after approval it returns to the exact Remote context in Work Home.
15. Cluster settings, Personal settings, Account usage, Code reviews, and sign
    out live in the Account menu. Cluster settings is admin-only.
16. An unmaterialized provider Repository still exposes the full Workspace.
    Empty Automations and Usage retain their normal structure, Usage begins at
    zero, and Settings shows inherited/default policy values. The UI never asks
    the user to run a first task merely to reveal those sections.

## HTTP contract

The initial Repository facade uses the existing stable Repository id while the
storage implementation may still resolve it to a Service row.

```text
GET    /api/v1/account/repositories?q={query}&limit={1..50}
POST   /api/v1/account/tasks

GET    /api/v1/repositories
GET    /api/v1/repositories/{repositoryID}
PATCH  /api/v1/repositories/{repositoryID}
DELETE /api/v1/repositories/{repositoryID}

GET    /api/v1/repositories/{repositoryID}/branches
POST   /api/v1/repositories/{repositoryID}/runs
GET    /api/v1/repositories/{repositoryID}/runs
GET    /api/v1/repositories/{repositoryID}/usage

GET    /api/v1/repositories/{repositoryID}/agent-board
PUT    /api/v1/repositories/{repositoryID}/agent-board
DELETE /api/v1/repositories/{repositoryID}/agent-board
GET    /api/v1/repositories/{repositoryID}/agent-board/policy
GET    /api/v1/repositories/{repositoryID}/agent-board/card-executions
POST   /api/v1/repositories/{repositoryID}/agent-board/occurrences
```

`GET /api/v1/account/repositories` is the bounded composer catalog. It lists
the first 12 provider repositories by default; `q` performs provider search and
`limit` may request at most 50 results from each linked provider. The endpoint
never walks every provider page as part of Work Home rendering. It includes
`repository_id` only when an internal Repository detail already exists. A
provider failure is reported as an unavailable source; it is not replaced with
a fake catalog. The Console may show a fresh TanStack Query cache entry while
it revalidates in the background and uses skeletons when no cache exists.

After selection, branches and task creation resolve the provider repository by
its stable numeric id. That lookup is one provider request and must not replay
the Account catalog scan.

`POST /api/v1/account/tasks` accepts provider, provider repository id, prompt,
branch, model, and session options. The server verifies current Account access,
creates or reuses the hidden personal Project and Repository binding, freezes
the Account-authorized provider credential, and creates the Run as one public
operation. Unsupported provider execution and revoked credentials return typed,
actionable errors.

`POST .../occurrences` accepts the canonical JType workspace, document id/path,
and an idempotency key generated by the host UI. It returns `202` with the
created or existing occurrence. It never returns a fabricated Run id before a
Run exists.

The Agent Board write body includes:

```json
{
  "installation_id": "installation_id",
  "board_ref": "path/to/board.board",
  "trigger_column": "agent",
  "work_column": "doing",
  "done_column": "done",
  "model_id": "model_id",
  "model_effort": "high",
  "execution_account_id": "user_id",
  "enabled": true
}
```

`done_column` may be empty. `model_id` and `execution_account_id` are required
when enabling. The server validates that the model is directly authorized for
the execution account; Project grants are not an authorization substitute.

## Occurrence state machine

```text
eligible Card
  ├─ transition into Agent queue
  └─ Run with agent
          │
          ▼
received occurrence ── duplicate key ──> same occurrence
          │
          ├─ dependency unavailable ──> blocked ── repair ──┐
          │                                                 │
          └─ claim Card / move work column <────────────────┘
                                  │
                                  ▼
                             queued Run
                                  │
                         scheduling / running
                                  │
              ┌───────────────────┼───────────────────┐
              ▼                   ▼                   ▼
        code_changes        no_changes          failed/canceled
              │                   │                   │
              └──── successful Delivery/writeback ───┘
                                  │                   │
                         optional move Done      stay in work
```

An active occurrence disables **Run with agent**. A terminal occurrence can be
run again with a fresh idempotency key; triggering from Done requires explicit
confirmation and moves the Card to work. Card edits after the Workflow Contract
is frozen apply only to the next occurrence.

Blocked readiness is retried in place before a Run exists. A failed Run is not
automatically replaced by a second Run. Board disconnect/rebind returns
`409 active_occurrences` while any occurrence is non-terminal or has pending
writeback.

## Completion and Delivery

The Run result has a structured completion kind:

- `code_changes` — code changed and declared Delivery completed;
- `no_changes` — the Agent concluded the task without a source diff and
  supplied a summary/evidence;
- `failed` — execution did not satisfy the task.

Both successful kinds can move to Done. The Done transition occurs only after
control-plane Delivery and the terminal receipt comment succeed. A failed
JType writeback remains durable and retryable; the UI displays `writeback_pending`
instead of claiming completion.

## Execution account and model authorization

`execution_account_id` is persisted on the Repository Agent Workflow. Enabling
defaults it to the Repository owner. Dispatch requires:

- the account still exists and still owns the Repository;
- the selected model is directly present in `model_account_grants` for that
  account;
- the selected effort remains supported; and
- the Repository/Provider/JType/Runner prerequisites are ready.

An ownership change blocks the Workflow with `execution_account_not_owner`
until the owner saves the Agent Board again, which applies the new default.
Revoked authorization produces a typed `model_not_authorized` blocker; there is
no fallback model.

## Shared Conversation contract

The rendering ownership is:

```text
jcode-ui-core
  RuntimeState + RuntimeActions + ThreadItem + Conversation capabilities
        │
jcode-ui
  Thread + Message + tools + approvals + pending + composer + scrolling
        │
        ├─ jcode Desktop/Web adapter (RTK + WebSocket)
        └─ Cloud adapter (Run SSE / device relay / REST actions)
```

Host-specific data must enter through typed extension seams. Cloud ACP approval
option ids and attributed multi-user messages are valid extension cases; copying
the renderer is not. Unsupported actions are omitted or visibly disabled by
runtime capabilities, never rendered as dead controls.

Conversation projections are memoized from ordered event snapshots. The Cloud
adapter must not regroup or reparse the entire event list for unrelated page
state changes, and list/runtime snapshots must retain referential identity when
their content has not changed.

## Performance and caching

Authoritative occurrence, Run, Delivery, permission, and writeback state is
never served from an application cache.

Safe caches use cache-aside with bounded TTL and explicit invalidation:

| Data | Cache | TTL / invalidation |
| --- | --- | --- |
| materialized model provider config | existing `modelcfg.Resolver` | 3s + catalog/grant invalidation |
| JType Board schema/column metadata | orchestrator bounded cache | 30s + Agent Board/Plugin writes |
| Repository detail and Agent Board policy | React Query | 15s; invalidate on mutation/SSE terminal event |
| Conversation event projection | host memoized snapshot | invalidated by ordered event cursor only |

Errors and authorization failures are never cached. Cache keys include tenant,
Repository, installation/config revision, workspace, and canonical Board id.
The Board cache is size-bounded and uses one in-flight load per key to prevent a
picker or reconnect burst from fanning out to JType.

Database access paths retain these indexes:

- one active Kanban Automation per Repository;
- one Repository per `(installation_id, board_ref)`;
- Card history by `(repository_id, workspace_id, document_path, created_at)`;
- dispatch retry by `(state, updated_at)`; and
- execution-account model grants by `(user_id, model_id)`.

## UI information architecture

The personal Console uses one Work Home information architecture:

```text
Work Home
  ├─ Account prompt composer
  │    └─ upper-left context picker
  │         ├─ Repository: every repository visible to linked Git accounts
  │         └─ Remote: online jcode devices and a Remote onboarding link
  └─ selected Repository workspace
       └─ Tasks | Board | Reviews | Automations | Usage | Settings

Account menu
  ├─ Code reviews
  ├─ Personal settings (profile, Git accounts, models, usage, preferences)
  ├─ Account usage
  ├─ Cluster settings (Cluster Admin only)
  └─ Sign out

Remote onboarding (`/devices/guide`)
  └─ jcode login → device-code authorization → encrypted pairing → Work Home
```

Agent Board displays status, fixed automation model/effort, Cloud execution,
trigger/work/done flow, dependency health, and an **Open board** action. The
Board is a Repository-scoped work mode, not a Project-wide list of boards. Card
detail shows **Run with agent**, current occurrence, blocker,
Run/Conversation link, Delivery/writeback, usage, and prior occurrences.
Repository Usage calls the Repository-scoped endpoint and never relabels a
hidden-container aggregate as Repository data.

Repository selection does not require a gear, existing internal Repository, or
connection flow. Repository detail is the lower part of Work Home after a
selection; it is not a separate product page. The Composer remembers its last
interactive model per account; changing it never mutates the Agent Board model.
Selecting Remote hides the Repository tabs and renders the shared jcode
composer. The old Project, Service, Repository-list/detail, and Remote-device
management pages have no active Console route; legacy deep links redirect to
Work Home or Remote onboarding.

## Test design before implementation

### API and store

1. Repository routes work and the removed Service routes return 404.
2. one Repository/one Board and one Board/one Repository conflicts return 409.
3. enabling requires an existing Board, distinct required trigger/work columns,
   optional distinct Done, explicit model, and authorized execution account.
4. account grant revocation produces `model_not_authorized` without fallback.
5. **Run with agent** and a durable transition converge on the same occurrence
   classifier; duplicate idempotency keys never create a second Run.
6. active occurrences block duplicate button execution and Board disconnect.
7. `code_changes` and `no_changes` move Done only after Delivery and receipt;
   failed/canceled stay in work.
8. writeback failure remains pending and resumes idempotently.
9. Repository/Card/occurrence authorization is tenant-scoped.
10. migration round-trips new fields in memory and PostgreSQL stores.
11. Account repository catalog includes unmaterialized provider repositories;
    first task materializes once and later tasks reuse the same Repository.
12. service principals cannot use Account composer routes, and provider access
    loss fails visibly without creating a Run.

### Cache and performance

1. repeated Board schema reads hit JType once inside TTL;
2. Agent Board or Plugin mutation invalidates the relevant key;
3. errors and authorization failures are not cached;
4. concurrent cache misses coalesce to one upstream request;
5. cache capacity evicts old entries and never grows without bound;
6. Repository policy p95 remains below 500ms in the local load smoke.

### Console and shared Conversation

1. the Account composer is the new-task entry, lists every Account repository,
   and shows no connect-Repository prerequisite or Project/Service selector;
2. setup connects only existing Boards and requires model/work column;
3. Done may be empty and no-change success is represented as successful;
4. **Run with agent** is disabled for an active occurrence and creates an
   occurrence rather than a direct Run;
5. policy errors are typed and actionable;
6. Cloud Run and Device conversations render through shared `jcode-ui` Thread
   behavior, including Markdown, tools, pending state, approval seams, and
   follow-scroll;
7. changing account last-used composer model leaves Agent Board configuration
   unchanged.

### Local and production e2e

1. connect an existing JType Board to one Repository;
2. trigger one Card by column and one by **Run with agent**;
3. prove one occurrence/one Run for replayed input;
4. prove `no_changes` writes a receipt and moves Done;
5. open the Run and verify the same Conversation renderer contract used by
   Desktop;
6. revoke the account model grant and observe a visible blocker with no Run;
7. restore the grant and observe the same occurrence resume;
8. capture API latency/cache evidence and check public production health/logs.

## Rollout

The orchestrator deploys before Console because the new Console uses Account
and Repository routes. The schema migration is append-only/idempotent. Rollback is
the previous application image; new nullable columns and indexes remain safe.
Because Service routes are intentionally removed, orchestrator and Console are
released in the same image workflow and the public smoke must verify both before
the rollout is accepted.
