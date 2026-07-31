# Automation execution ledger and Card output

Status: Loop 3 implementation contract

Parent: `docs/22-jtype-agent-work-prd.md`

## Outcome

An Automation is no longer only configuration plus `last_error`. Every trigger
decision becomes an immutable execution that answers:

- what triggered;
- whether Cloud accepted, ignored, blocked, superseded, or dispatched it;
- which Run, pull request, or jtype Card is the output;
- who can repair a blocked execution;
- whether an external writeback is complete.

Service Kanban remains a Service capability. Its Card execution history stays
on the Card and is not duplicated in the Project Automation list.

## User and jobs

The primary user is a Project member operating unattended work. They need to
know whether an Automation did nothing by design, could not run, or is still in
progress without reading orchestrator logs.

- Viewer: inspect history and follow output links.
- Member: use **Run now** with a client-generated idempotency key.
- Owner: edit trigger, output, model, and repair unhealthy dependencies.
- Cluster Admin: repair Provider/model configuration, without gaining Project
  mutation rights.

## Execution truth

`automation_executions` is the durable control-plane occurrence. Source
receipts remain authoritative evidence:

- SCM keeps the normalized `webhook_receipts` row;
- Cron keeps its compare-and-swap fire cursor;
- a create-card execution keeps a deterministic Card path and materialization
  state;
- Kanban keeps its independent Card claim and transition occurrences.

The public state vocabulary is:

| State | Meaning |
| --- | --- |
| `accepted` | occurrence recorded; an output is being materialized |
| `ignored` | valid trigger intentionally did not dispatch |
| `duplicate` | a replay resolved to an existing occurrence |
| `superseded` | a newer coalesced occurrence replaced this queued work |
| `blocked` | an actionable dependency prevented output |
| `queued` | linked Run is queued |
| `running` | linked Run is active |
| `terminal` | linked Run reached succeeded, failed, or canceled |

`running`, `terminal`, and `superseded` are projections of the linked Run when
possible, including a `create_card` Run reached through the Card claim and when
the API applies a state filter. A canceled Run whose
phase is `Superseded` is not shown as a generic failure.

Webhook delivery/payload replays resolve through the source receipt and return
the original occurrence; they do not create a second ledger row or rewrite the
original row to `duplicate`. Distinct SCM occurrences may share a Run
`coalesce_key`; an older queued Run then projects as `superseded`. A receipt
whose ledger write failed is reclaimable so a Provider retry can finish the
same delivery instead of being mistaken for completed work.

Every reason is sanitized and typed (`reason_code`, `message`, `repair_role`).
No raw webhook body, prompt/response payload, header, token, or encrypted
credential enters the ledger.

## Outputs

| Trigger / run kind | Output |
| --- | --- |
| SCM `review` | Run plus provider-native review / pull request |
| SCM `agent` | direct Run |
| Cron `run_only` | direct Run |
| Cron `create_card` | deterministic jtype Card, then normal Service Kanban |
| Manual Run now | the Automation's configured output |

`create_card` requires an enabled, healthy Service Kanban binding. It writes a
Card directly into that binding's trigger column. The Cron dispatcher never
creates a Run for the same execution.

## Idempotency and crash recovery

### Manual

The client sends a UUID-like idempotency key. `(automation_id, event_key)` is
unique and retained permanently, exceeding the required 24-hour retry window.
Concurrent submissions return the same execution and Run.

### Cron

The event key uses the scheduled fire boundary rather than poll time:
`cron:<automation_id>:<scheduled_fire_at>`. Claiming the Cron cursor, inserting
the execution, and optionally inserting the direct Run are one Store
transaction.

### create_card

The Card path is derived from the execution id:

`jcode-automation/<automation_id>/<execution_id>.md`

The body contains a stable, non-secret execution marker. Materialization is a
small state machine:

1. `planned` is persisted with the execution.
2. Cloud atomically claims it as `creating` before the external save.
3. If the path already exists and carries the same marker, Cloud binds it.
4. If the path exists with another marker, the execution is blocked
   `card_path_conflict`.
5. After `SaveDocument`, Cloud resolves the same path and binds its document id.
   A lost save response or a temporarily invisible/unreadable path remains
   `creating`; it is visible as blocked but stays eligible for recovery.
6. A restarted `creating` execution resolves the path; it never blindly saves
   again. If the path is absent, the result is `card_creation_uncertain`, not a
   duplicate Card.
7. Once bound, normal jtype events create the Service Kanban occurrence and
   Run. The Automation history projects that linked Run through the Card claim.
8. A later missing Card is `card_unavailable`; it is never recreated.

The Automation ledger keeps polling blocked `create_card` executions because
materialization dependencies may recover. Blocked direct-Run decisions are
terminal ledger facts and do not poll indefinitely.

The board proxy treats this namespace as managed: a Member may edit or move an
existing, same-board Automation Card, but cannot create a new document under
`jcode-automation/`. Only the materializer creates those paths.

This deliberately prefers a visible ambiguous failure over fabricating
exactly-once success across Cloud and jtype.

## API

- `GET /api/v1/automations/{id}/executions`
  - Viewer
  - newest first, cursor pagination
  - optional `state` filter
- `GET /api/v1/automations/{id}/executions/{execution_id}`
  - Viewer
- `POST /api/v1/automations/{id}/executions`
  - Member
  - `{ "idempotency_key": "..." }`
  - `201` for a new execution, `200` for an idempotent replay
- Cron configuration adds `output_mode: run_only | create_card`.

Each execution view contains trigger, state/outcome, sanitized reason, actor
snapshot, Run/Card/PR output refs, writeback state, and timestamps. Usage is
explicitly `unavailable` until Loop 4 supplies a summary; it is never rendered
as zero.

## UI

`design/automation-executions.html` is the page-scoped reference.

The Automation detail page puts operational truth before configuration:

1. title, enabled state, trigger summary, **Run now**, Edit;
2. filterable execution ledger;
3. a selected execution inspector with Trigger, Output, Accountability,
   Writeback, and Usage;
4. concise repair action for blocked rows;
5. configuration summary last.

On narrow screens, the ledger becomes stacked cards and the inspector follows
the selected card. State is never conveyed by color alone.

The editor exposes Cron output as a two-option choice. `create_card` is disabled
when Service Kanban policy is missing or blocked, with the policy blocker and
repair role shown inline.

Card output links use the canonical Project workspace route and hand the exact
document path to `jtype-board-react`. A Viewer can resolve the linked board and
read the Card through the proxy, but the embed is read-only and every proxy
write still requires Member. The Card detail polls while it has no occurrence
so an accepted or blocked receipt appears after the Card enters the trigger
column; polling stops once all known occurrences are terminal.

## Test design (written before implementation)

### Store contract

1. concurrent Manual submissions create one execution and one Run;
2. event-key replay returns the existing execution;
3. Cron cursor + execution + direct Run commit atomically;
4. blocked Cron commits an execution without a Run;
5. newest-first cursor pagination is stable for equal timestamps;
6. state filtering cannot leak another Automation's execution;
7. automation deletion cannot rewrite historical snapshots;
8. PostgreSQL round-trips nullable Card/Run/reason/actor fields.

### Dispatcher

1. SCM match records queued output; filter/ignore records ignored; every
   post-match dependency/authorization exit records blocked, and a failed
   ledger write leaves a reclaimable error receipt;
2. model/provider/service failures record blocked with repair role;
3. Cron `run_only` produces exactly one Run;
4. Cron `create_card` produces no direct Run;
5. healthy create-card saves deterministic path in trigger column;
6. crash, lost save response, or post-save read failure recovers by binding the
   existing marker/path without a second save;
7. `creating` with missing path blocks uncertain and never calls save;
8. path collision, JType unavailable, deleted Card, and disabled Kanban are
   visible blocked/unavailable states.

### API and UI

1. Viewer can list but cannot Run now;
2. Member double-submit gets `201`, then `200`, with the same ids;
3. output links resolve to Run/Card/PR without returning secrets;
4. legacy/empty history says “No executions yet”, not “0 runs”;
5. blocked, ignored, superseded, running, and terminal rows are distinct;
6. mobile preserves state, reason, time, and primary output;
7. create-card option is disabled with a visible repair action when Kanban is
   unhealthy;
8. the output URL preserves an encoded Card path and uses a valid workspace
   tab;
9. Viewer can read the linked Board/Card but cannot save, while Member may
   update an existing managed Automation Card and cannot forge a new one;
10. an empty Card detail continues polling until its first execution receipt
    appears.
