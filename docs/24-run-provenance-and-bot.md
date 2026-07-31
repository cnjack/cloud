# Run provenance and Bot identity

Status: Loop 2 implementation contract
Parent: `docs/22-jtype-agent-work-prd.md`

## Outcome

A Run answers four different questions without collapsing them into one
ambiguous “actor”:

1. **Requested by** — the direct Cloud member or external actor that caused the
   work;
2. **Accountable to** — the Cloud member responsible for the rule when there is
   no direct human request;
3. **Executed by** — the Cloud runtime principal, Service and frozen model;
4. **Written back as** — the provider App/Bot that publishes comments, reviews
   or Card receipts.

The projection is read-only audit data. `triggered_by_user_id` remains the only
existing direct-user identity used by authorization and credential selection.
Neither accountable actors, external actors, display labels nor Bot identity
may be consumed by authorization code.

## Source matrix

| Run source | Requested by | Accountable to | Precision | Written back as |
| --- | --- | --- | --- | --- |
| Console/API member | Cloud member snapshot | same member | `exact` | provider Bot only when output is published |
| API key/service principal | — | — | `unattributed` | provider Bot when applicable |
| SCM mapped actor | Cloud member + linked provider actor | direct member, otherwise rule author | `exact` | repository App/Bot |
| SCM unmapped actor | external provider actor | rule author | `linked_external` or `rule_owner` | repository App/Bot |
| Cron Automation | — | rule author | `rule_owner` | provider Bot when applicable |
| jtype Kanban | jtype `editedBy` display snapshot only | rule author | `linked_external` or `rule_owner` | jcode Cloud Bot for JType |
| Legacy record | best derivation from retained Run facts | best derivation | `unattributed` when evidence is incomplete | only when source proves it |

`editedBy` is never name-matched to a Cloud user. A missing/deleted member keeps
the stored display snapshot and is labelled as a former member.

## Stable snapshot and resolver

Each new Run stores a bounded provenance snapshot containing actor references,
attribution source/precision, runtime principal and writeback identity. It
contains no tokens, webhook payloads, prompts or provider response data.

The API resolver combines that snapshot with retained Run facts and current
Project/Service labels:

- snapshots win for identity and attribution;
- old Runs receive an explicit best-effort fallback without mutating them;
- missing current Service/Project data becomes `unavailable`, never a fabricated
  label;
- trigger references use stable Card/PR/Automation ids or URLs;
- model uses the Run dispatch-time `model_name` snapshot.

A retry or resumed session is a new execution requested by the member who
performed that action. It therefore receives a new direct-request snapshot;
`retried_from` / `resumed_from` preserve the original execution chain. Copying
the old requester would erase the member who actually caused the new execution.
This does not rebind the new Run to the original external occurrence.

## API

`GET /api/v1/runs/{id}` adds:

```json
{
  "provenance": {
    "requested_actor": {
      "kind": "external_actor",
      "label": "Mei",
      "provider": "jtype"
    },
    "accountable_actor": {
      "kind": "cloud_user",
      "id": "user_…",
      "label": "Jack"
    },
    "attribution_source": "kanban_event",
    "precision": "rule_owner",
    "trigger": {
      "kind": "kanban_card",
      "label": "JType Card",
      "ref": "occurrence_…"
    },
    "executed_for": {
      "project_id": "project_…",
      "project_label": "commerce",
      "service_id": "service_…",
      "service_label": "payments-api",
      "repository": "acme/payments",
      "model": "anthropic/claude-sonnet"
    },
    "runtime_principal": {
      "kind": "automation_principal",
      "label": "Cloud Automation"
    },
    "writeback_actor": {
      "kind": "provider_bot",
      "label": "jcode Cloud Bot",
      "provider": "jtype"
    }
  }
}
```

Viewer may read this projection. No provenance field changes mutation
authorization.

## UI

The Run inspector adds an **Identity & source** section before technical
execution facts:

- Requested by and Accountable to are adjacent, with `External` or `Rule owner`
  qualification where necessary;
- Triggered from links to a stable external reference when one exists;
- Written back as is visually separated and always says “Bot/App”;
- an expandable technical row shows runtime principal and precision;
- missing data says “Unavailable” or “Not attributed”, never `0`, blank success
  or a guessed member.

On widths below 700px the identity section becomes a single-column block below
the conversation. Requested/Accountable stay above runtime and Bot details.

## Test cases

1. manual member: same frozen member in Requested and Accountable;
2. Cron: no Requested actor, rule author Accountable;
3. mapped SCM: Cloud member plus linked external provider identity;
4. unmapped SCM: external actor, never an arbitrary Project owner as requester;
5. Kanban `editedBy`: display-only external actor, rule author accountable;
6. Bot appears only in Written back as;
7. deleted member keeps the snapshot label;
8. old Run fallback is explicit and never upgrades precision;
9. Viewer can read provenance;
10. changing accountable/display/Bot values cannot change authorization tests;
11. JSON snapshot round-trips in Memory and PostgreSQL without secrets.
