# Kanban execution receipts

Status: implementation contract for Feature Loop 1

Parent product contract: [22-jtype-agent-work-prd.md](22-jtype-agent-work-prd.md)

Prototype: [../design/kanban-agent-executions.html](../design/kanban-agent-executions.html)

## Outcome

Moving a jtype Card into a Service Kanban trigger column becomes an observable,
idempotent request for Cloud execution:

1. Cloud durably records the transition before it attempts dispatch.
2. The Card immediately shows whether the request was accepted or blocked.
3. The same event, a bootstrap rescan, or an edit while the Card remains in the
   trigger column cannot create another Run.
4. A Card that leaves and later re-enters after the prior execution and
   writeback are terminal creates a new execution without losing the permanent
   Card-to-Service relationship.
5. Both the embedded board and an external jtype client can understand the
   result. The embedded detail uses a Cloud projection; the external Card gets
   a marker-backed comment.

The Card remains the Work Item truth. A Run remains execution truth. No Run
state is copied into Card frontmatter.

## Scope

This loop implements:

- Service Kanban policy preview and dependency health;
- a durable jtype event cursor with a one-time level bootstrap;
- permanent Card claim anchors and repeatable transition occurrences;
- accepted, blocked, active-run, failed, canceled, succeeded, and
  writeback-pending receipts;
- idempotent Card comments and terminal writeback;
- a paginated Card executions API;
- a `jtype-board-react` Card-detail supplement slot;
- the embedded Cloud executions panel, loading/empty/error/blocked/history
  states, Run links, and a keyboard-accessible path.

This loop deliberately does not implement:

- stable requested/accountable identity resolution beyond honest actor
  snapshots; that is Loop 2;
- generic Automation history or Cron/SCM output; that is Loop 3;
- token/cost aggregation; that is Loop 4;
- a pre-move blocking confirmation. `jtype-board-react` does not currently
  expose an optimistic-lock-safe pre-save/cancel hook. The policy preview and
  receipt are therefore non-blocking and server-authoritative.

## Invariants

1. `(automation_id, document_id)` identifies one permanent claim anchor.
2. One valid transition into the trigger column identifies one occurrence.
3. One occurrence owns at most one Run.
4. One effective jtype event key owns at most one occurrence for an Automation.
5. A blocked occurrence is resumed in place after repair; it is not replaced.
6. An active occurrence prevents a concurrent Run for the same claim even if
   the Card leaves and re-enters.
7. A new occurrence is allowed only after:
   - the claim was observed outside the trigger column;
   - the previous occurrence is terminal; and
   - any required terminal writeback is complete.
8. A comment projection is idempotent by occurrence and receipt phase.
9. Disabling or deleting the binding stops new occurrences but does not erase
   claims, occurrences, frozen routing, or in-flight writeback.
10. `editedBy` is display text, not a Cloud principal or authorization input.

## State model

### Trigger cursor

`automation_kanban_triggers` gains:

| Field | Meaning |
| --- | --- |
| `event_cursor` | Last fully applied durable jtype board event sequence |
| `bootstrapped_at` | Non-null after the one-time level scan has established initial state |

The poller consumes events in sequence order and advances the cursor only after
the event has been durably classified. On first adoption of this contract it
performs one level scan:

- Cards outside the trigger column establish `last_observed_column`;
- Cards already inside establish one `bootstrap:<automation>:<document>`
  occurrence;
- subsequent ticks consume only durable events plus retryable work.

A board that cannot provide events is a visible `event_feed_unavailable`
binding blocker; repeatedly scanning levels is not an acceptable steady state.

### Claim anchor

`automation_kanban_claims` remains keyed by `(automation_id, document_id)` and
stores stable external identity plus re-entry state:

| Field | Meaning |
| --- | --- |
| existing frozen installation/workspace/path/done fields | Writeback route that survives later binding deletion |
| `last_observed_column` | Latest classified Card column |
| `outside_trigger_at` | Latest observation outside the trigger column |
| `latest_occurrence_id` | Convenience pointer, not the occurrence identity |
| `external_ref_available` | False after an authoritative Card-not-found response |
| `updated_at` | Last state observation |

The historical `run_id` and `writeback_at` columns remain readable during the
transition, then are treated as compatibility projections of the latest
occurrence. New behavior never uses `claim.run_id != null` as a permanent
“already ran forever” switch.

### Occurrence and receipt

Migration `0058` adds `automation_kanban_occurrences`:

| Field | Contract |
| --- | --- |
| `id` | Stable Cloud occurrence id |
| `automation_id`, `document_id` | Permanent claim key snapshot |
| `event_key` | Unique per Automation; `event:<sequence>` or deterministic bootstrap key |
| `event_sequence` | Nullable for bootstrap |
| `actor_display` | Untrusted jtype display snapshot only |
| `entry_column` | Trigger column snapshot |
| `state` | `received`, `blocked`, `queued`, `running`, `terminal` |
| `outcome` | Empty until terminal, then `succeeded`, `failed`, or `canceled` |
| `reason_code`, `reason_message` | Typed blocker/failure and safe guidance |
| `repair_role` | `project_owner`, `cluster_admin`, or empty |
| `run_id` | Nullable, unique when present |
| `receipt_phase` | Latest required external comment phase |
| `receipt_written_at` | Latest external receipt projection |
| `writeback_state` | `not_required`, `pending`, `complete`, `unavailable` |
| `writeback_error` | Safe last error, never a credential or model payload |
| frozen installation/workspace/path/done fields | Route for later receipt/writeback |
| timestamps | `created_at`, `updated_at`, `terminal_at` |

Required constraints:

- unique `(automation_id, event_key)`;
- unique `run_id` where non-null;
- foreign key to Run uses `ON DELETE SET NULL`;
- occurrence rows do not cascade with Automation, Service, Project, or Plugin
  deletion; historical execution and frozen writeback must survive.

Receipt is the observable state of the occurrence, not another independently
mutable aggregate. The API derives receipt status from occurrence fields.

### Transition classifier

| Observation | Result |
| --- | --- |
| First bootstrap inside trigger | Create claim + bootstrap occurrence |
| Enter trigger with unseen sequence, no active occurrence, prior writeback complete | Create occurrence |
| Replay of the same sequence | Return existing occurrence |
| Edit while still in trigger | Update claim observation; no occurrence |
| Level rescan after bootstrap | No occurrence |
| Leave trigger | Mark claim outside; keep occurrence |
| Re-enter while prior Run active | No Run; project `already_running` receipt |
| Re-enter after prior terminal but writeback pending | No Run; project `writeback_pending` receipt |
| Re-enter after prior terminal + writeback complete + observed leave | Create occurrence |
| Dependency missing before dispatch | Same occurrence becomes blocked |
| Dependency repaired | Resume same blocked occurrence |

The classifier is one deep module used by event consumption, bootstrap, and
blocked retry. Handlers do not reproduce these rules.

## Dispatch and receipt order

For a valid entry:

1. Lock or atomically compare the claim.
2. Insert/get the occurrence by event key.
3. Persist `received` before any external or Run side effect.
4. Resolve the binding, Service, repository, model, runner capacity, and jtype
   writeback route.
5. If a dependency is absent, persist `blocked` with a typed reason and repair
   role; enqueue/retry its Card comment.
6. Otherwise create/get the Run using occurrence id as its idempotency key,
   attach it to the occurrence, and persist `queued`.
7. Write the accepted receipt comment. A temporary comment failure does not
   roll back the accepted Run; it leaves receipt writeback pending.
8. Reconciliation projects Run state into the occurrence.
9. Terminal reconciliation writes a terminal comment. Success may then move
   the Card to the frozen done column; failure and cancel never move it.
10. Only after all required terminal projections succeed is writeback complete.

Blocked dependencies are retried from stored occurrence state even without a
new board event. Model/provider/runner failures remain visible and never produce
fake Run success.

## External Card comment

Each phase has a stable, hidden marker:

```html
<!-- jcode-cloud-occurrence:occ_123:accepted -->
```

The visible comment is concise:

> jcode Cloud accepted this Card for `payments-api` using `claude-sonnet`.
> Run: `run_123`.

Blocked comments name the dependency and next owner:

> jcode Cloud could not start this Card: no model is configured for the
> Service. Project owner: choose an allowed model; cluster admin: configure the
> provider.

Before creating a comment the client checks existing Card comments for the
exact marker. A timeout after a remote success therefore retries safely. Comment
creation success and Card movement are tracked separately.

## HTTP contract

### Policy

`GET /api/v1/services/{service_id}/kanban/policy`

Member-readable response:

```json
{
  "service_id": "svc_123",
  "service_name": "payments-api",
  "repository": "acme/payments",
  "model": {"id": "model_123", "label": "Claude Sonnet"},
  "board": {"workspace_id": "ws_123", "ref": "jcode.board"},
  "trigger_column": {"key": "ai", "label": "Agent queue"},
  "done_column": {"key": "done", "label": "Done"},
  "output": "comment_and_move_on_success",
  "health": {
    "state": "ready",
    "blocker": null
  }
}
```

Unavailable dependencies return a successful policy projection with
`health.state = "blocked"` and a typed blocker when the binding itself exists.
Missing/unauthorized resources continue to use the shared typed error envelope.
The UI must not replace a blocked policy with a healthy-looking default.

### Card executions

`GET /api/v1/services/{service_id}/kanban/card-executions`

Query:

- `workspace_id` required;
- `document_path` required because `BoardViewCard.id` is the relative path in
  `jtype-board-react`;
- `before` optional opaque cursor;
- `limit` defaults to 20 and is capped.

The server resolves the binding and claim. The browser never supplies an
Automation id or document id as authority.

Response:

```json
{
  "claim": {
    "document_path": "jcode/fix-retry.md",
    "external_ref_available": true
  },
  "items": [
    {
      "id": "occ_123",
      "status": "running",
      "summary": "Run is applying the requested change",
      "reason": null,
      "repair_role": null,
      "requested_actor": {"label": "Jack", "precision": "display_only"},
      "run": {
        "id": "run_123",
        "status": "running",
        "href": "/projects/p_123/runs/run_123"
      },
      "receipt": {"external": "written", "writeback": "pending"},
      "created_at": "2026-07-31T02:00:00Z"
    }
  ],
  "next_cursor": null
}
```

Items use newest-first fixed ordering `(created_at DESC, id DESC)`. They expose
safe summaries, not prompts, model bodies, Plugin tokens, or raw jtype events.
An absent claim returns `items: []`; it is not an error. An authoritative
deleted Card returns preserved history with `external_ref_available: false`.

## jtype package contract

`jtype-board-react` adds:

```ts
type JTypeBoardProps = {
  renderCardSupplement?: (card: BoardViewCard) => ReactNode
}
```

The function is invoked only for the built-in editable Card detail and its
result is rendered in a dedicated, labelled-neutral slot beneath native
Properties. It is not invoked when `onCardOpen` intercepts the detail.

Shared board components receive the value through slots:

- `BoardSurfaceProps.renderCardSupplement`;
- `BoardPeekProps.supplement`.

No platform check is added. Omission preserves Desktop, Web, package bundle,
and existing hosts. The slot cannot block or mutate native title, description,
status, relations, comments, or Card save behavior.

The package README, public `.d.ts`, built bundle, fixture, and Playwright tests
are part of the release. Feature impact:

| Surface | Effect |
| --- | --- |
| Desktop | No visible change when prop omitted |
| Web | No visible change when prop omitted |
| `jtype-board-react` editable embed | Optional supplement in native detail |
| read-only embed | Unchanged |
| intercepted `onCardOpen` | Unchanged; host owns the detail |

## Embedded UX contract

Above the board, the policy strip always answers:

- which column requests execution;
- Service and repository;
- selected model;
- terminal output;
- whether the path is currently ready or blocked.

Inside the Card detail, the supplement is ordered for triage:

1. current receipt state and the next action;
2. Run link, or blocker and responsible role;
3. truthful request actor snapshot;
4. collapsed prior occurrences.

Loading uses a small labelled skeleton. Empty says that no Cloud execution has
been requested and tells the user which column triggers one. API error remains
visible with Retry. A blocked result is not styled as progress or success.

Keyboard users can use the board's existing status selector to move a Card and
can tab to Retry, Run, and history disclosure. The supplement uses semantic
status/alert regions and never relies on color alone.

At narrow width, the native Card detail already stacks its inspector below the
editor. The supplement stays inside that inspector; current receipt precedes
history and technical ids wrap instead of widening the dialog.

## Test design

### jtype red-green cases

1. Editable embed opens native Card editor and renders host supplement.
2. Native Description and Properties remain usable with the supplement.
3. Omitted supplement leaves Desktop/Web/embed output unchanged.
4. `onCardOpen` interception does not invoke or render the supplement.
5. Read-only detail remains unchanged.
6. Supplement survives bounded desktop and narrow viewport layouts.

### Store contract cases

Run against Memory and PostgreSQL:

1. first entry creates one claim and one occurrence;
2. event replay returns the same occurrence;
3. in-column edit and post-bootstrap level scan create nothing;
4. leave + terminal + writeback complete + re-enter creates a second occurrence;
5. active or writeback-pending re-entry does not create a Run;
6. blocked retry resumes the same occurrence;
7. concurrent consumers converge on one occurrence and one Run;
8. Automation/Service/Plugin deletion preserves run-bound history and frozen
   writeback;
9. Card deletion preserves history and marks the external reference unavailable;
10. event cursor advances only after durable classification.

### Poller and reconciler cases

1. ordered events, bootstrap, board drift, event feed unavailable;
2. model/provider/repository/runner gates produce typed blocked receipts;
3. accepted and terminal comments use stable markers;
4. comment timeout-after-success retries without duplication;
5. success comment then optional move; failure/cancel comment without move;
6. writeback partial failure remains pending and retries;
7. disabled binding stops new entries but completes frozen writeback.

### API and Console cases

1. Viewer/Member can read policy/history; outsider cannot.
2. document path is resolved only within the requested Service binding.
3. fixed ordering and cursor pagination are stable.
4. policy ready/blocked and Card panel loading/empty/error/blocked/running/
   terminal/deleted-source states render truthfully.
5. Run links target the bound Project.
6. keyboard-only status move and receipt inspection.

### E2E journey

The repository-owned fixture must prove:

1. external or embedded jtype Card enters the trigger column;
2. one occurrence, one accepted receipt, and one Run appear;
3. an in-column edit and replay do not add another;
4. terminal reconciliation comments and moves on success;
5. leaving then re-entering creates exactly a second occurrence.

## Delivery boundaries

1. jtype PR: additive Card supplement public contract, docs, package artifacts,
   Desktop/Web/package verification.
2. Cloud PR: this design, schema/state machine/poller/reconciler/API/Console/E2E.
3. Cloud depends on a reproducible published or immutable jtype package version;
   a machine-local tarball path is never committed.
4. Kimi CLI and Grok CLI review both PRs against PRD AC1 before final test and
   push. Confirmed findings are fixed, then the reviews are rerun.
