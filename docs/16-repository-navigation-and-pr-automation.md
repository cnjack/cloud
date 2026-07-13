# 16 · Repository Navigation and PR Review Automation

> Status: implementation design (2026-07-13).
>
> Scope: the provider repository action in the Service header and the
> event-driven PR review automation shown in `design/project-workspace.html`.

## 0. Decision summary

The approved Project workspace design is the product contract. Two missing
capabilities must be implemented below the Console instead of being omitted:

1. A provider-backed Service exposes a server-derived, browser-safe repository
   URL so the Service header can open Gitea, GitHub, or GitLab.
2. A Gitea-first PR review Automation persists its own instructions, model,
   event policy and enabled state. Gitea PR webhook events dispatch the existing
   `RunKindReview` execution path without requiring an `@jcode review` comment.

The comment command remains supported. It is an interactive trigger with the
commenter as its security principal; it is not the control plane for automatic
reviews.

GitHub and GitLab automatic PR events are not claimed by this change. Their
Automation create action returns a typed `automatic_review_unsupported` error,
and the Console explains that event-driven review is currently Gitea-first.
Their existing `@jcode` comment webhooks remain unchanged.

## 1. Repository navigation contract

`Service.repo_html_url` is a response-only projection. The server derives it in
this order:

1. the bound project Integration host;
2. the configured OAuth provider's external URL;
3. the provider public default (`github.com` or `gitlab.com`);
4. the configured Gitea URL as a legacy fallback.

The repository owner/name is appended only to an `http` or `https` base URL.
Raw repositories and invalid/unconfigured provider hosts return no URL. The
Console renders an external-link action only when the field is present; absence
is a visible unavailable state, never a dead link.

The projection is deliberately not persisted. A host rotation takes effect on
the next read, and a Service cannot retain a stale or client-supplied URL.

## 2. Domain model

### 2.1 Automation

```text
Automation
  id                 string
  service_id         string -> services.id
  name               string
  instructions       string
  trigger_type       "pr_review"
  model_id           string -> models.id
  events             [opened, ready, synchronize, reopened]
  base_branch        string
  include_drafts     boolean
  enabled            boolean
  last_triggered_at  timestamp?
  last_run_id        string? -> runs.id
  last_error         string
  created_by         string? -> users.id
  created_at         timestamp
  updated_at         timestamp
```

`model_id` is explicit because a headless Automation must not guess between
multiple grants. Create and enable operations validate the model immediately;
delivery processing validates it again because grants/configuration may change.

The existing `Schedule` aggregate remains backward-compatible. The Console
builds one Automation list projection from `Schedule` and `Automation` rows;
the provider-event implementation does not rewrite the proven schedule poller.

### 2.2 WebhookBinding

```text
WebhookBinding
  service_id            string -> services.id (primary key)
  provider              "gitea"
  endpoint              string
  status                pending | active | error
  last_synced_at        timestamp?
  last_delivery_at      timestamp?
  last_delivery_status  accepted | duplicate | ignored | error
  last_error             string
  updated_at             timestamp
```

The binding belongs to a Service, not an Automation: one provider hook delivers
comments and PR events for every Automation attached to that repository. Hook
secrets are never stored in this table or returned by the API.

### 2.3 Run provenance and idempotency

Runs add:

```text
origin                 api | webhook | kanban | schedule | automation
origin_automation_id   string?
origin_event_key       string?
```

For automatic reviews the event key is a deterministic SHA-256 digest of:

```text
automation_id + provider + repository + pr_number + head_sha
```

A partial unique index on `runs.origin_event_key` is the final concurrency
guard. Repeated provider delivery, two orchestrator replicas, and an
opened/synchronize pair for the same head commit cannot create duplicate Runs.
The existing comment-id uniqueness remains unchanged.

## 3. HTTP API

### 3.1 Read

```http
GET /api/v1/services/{service_id}/automations
```

Member+ response:

```json
{
  "automations": [],
  "webhook_binding": null
}
```

The binding is null until an owner successfully creates or synchronizes a PR
review Automation.

### 3.2 Create

```http
POST /api/v1/services/{service_id}/automations
```

Owner body:

```json
{
  "name": "Gitea PR automatic review",
  "instructions": "Review security, regressions, and fail-visible behavior.",
  "trigger_type": "pr_review",
  "model_id": "model-id",
  "events": ["opened", "ready", "synchronize"],
  "base_branch": "main",
  "include_drafts": false,
  "enabled": true
}
```

Before inserting, the API validates the Service/provider/model and uses only the
requesting owner's OAuth grant to reconcile the repository hook. It never asks
for a token in this request. Hook reconciliation failure returns a typed 409 or
502 and creates no Automation. A successfully reconciled hook is harmless if a
subsequent database insert fails and is reused on retry.

### 3.3 Update and delete

```http
PATCH  /api/v1/automations/{automation_id}
DELETE /api/v1/automations/{automation_id}
```

Owner-only. Enabling or changing event policy revalidates the model and
reconciles the hook before persisting the enabled state. Deleting the final
Automation does not remove the provider hook because the same hook still serves
the `@jcode` comment trigger.

## 4. Gitea provider adapter

Gitea's documented webhook event groups are used directly:

- `pull_request`: `opened`, `reopened`, and `edited`;
- `pull_request_sync`: `synchronized`;
- existing `issue_comment` and `pull_request_comment` for `@jcode` commands.

The repository hook reconciler lists hooks by target URL. If the hook exists but
lacks required events, it PATCHes the existing hook with the union of old and
required events. It does not return early merely because the URL exists.

`ready` maps to an `edited` payload only when the PR is currently non-draft and
the payload reports a previous draft value. Unknown edits are ignored.

## 5. Delivery flow

```text
Gitea
  -> HMAC validation
  -> provider payload normalization
  -> services matching provider + owner/name
  -> WebhookBinding last-delivery observation
  -> enabled Automation event/base/draft filters
  -> model revalidation
  -> deterministic origin_event_key
  -> CreateRun(kind=review, origin=automation)
  -> existing reconciler / runner / review comment writeback
  -> Automation last_run_id / last_triggered_at update
```

Failures before Run creation update `Automation.last_error` and the binding's
last-delivery status. Failures after Run creation remain first-class Run
failures and use the existing review writeback/retry observability.

The webhook HTTP response remains `200` after a valid signature so Gitea does
not amplify application errors into redelivery storms. Durable binding and
Automation state records whether the event was accepted, ignored, duplicated,
or failed.

## 6. Console information architecture

The Service header renders the approved external repository action beside the
Service utility area. It opens `repo_html_url` in a new tab with safe
`noopener noreferrer` semantics.

The Automations tab renders:

- the approved heading and create actions;
- All / Scheduled / PR review filters;
- PR review rows with name, instructions, events, base/draft policy, enabled
  switch, binding status and last delivery;
- schedule rows from the existing Schedule API;
- an inline create/edit surface, not a modal;
- typed dependency errors for missing OAuth, model, receiver configuration, or
  unsupported provider.

The old `WebhookSetupCard` is removed from the primary flow. Hook setup becomes
part of creating/enabling a PR review Automation, which is the user intent.

## 7. Test design

Tests are added before implementation.

| Layer | Case | Expected result |
| --- | --- | --- |
| API projection | Integration host, OAuth external URL, public provider default, no host | Safe `repo_html_url` or an explicit absence; never client-controlled JavaScript/data URL. |
| Store | Automation CRUD and binding round trip | Memory and Postgres contracts preserve events, model, timestamps, errors and ownership. |
| API RBAC | viewer/member/owner create and list | Member may list; only owner may mutate. |
| API validation | raw repo, non-Gitea provider, missing OAuth/receiver/model | Typed 409/400; no Automation row and no fake active binding. |
| Provider registration | missing hook, complete hook, incomplete existing hook | POST once, no-op when complete, PATCH existing ID with event union. |
| Payload parser | opened, reopened, synchronized, ready, unrelated edit | Correct normalized event or ignored status. |
| Dispatch filters | event mask, base branch, draft policy, disabled Automation | Only matching Automations create Runs. |
| Idempotency | duplicate and concurrent delivery for same head SHA | Exactly one review Run. |
| Run contract | accepted delivery | Review Run carries instructions, explicit model, PR refs, automation provenance and creator identity. |
| Binding state | accepted/duplicate/ignored/model error | Last delivery timestamp/status/error remains visible. |
| Console header | URL present/absent | Real external action or visible unavailable explanation; never a dead button. |
| Console Automation | list/filter/create/toggle/error | Matches the approved layout and exposes real server state. |

## 8. Acceptance criteria

- A provider-backed Service with a resolvable host opens its real repository.
- Creating a Gitea PR review Automation requires no pasted webhook token.
- Gitea opened/ready/synchronized/reopened events can dispatch the existing
  review runner according to the saved policy.
- `@jcode review` remains functional and independent.
- A repeated event for the same Automation, PR and head SHA creates no duplicate
  Run.
- The Automations UI never reports active/healthy/delivered without persisted
  server state.
- Every unsupported dependency is a typed visible state.
