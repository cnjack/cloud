# Project Plugin platform

Status: approved implementation contract

## Product contract

GitHub, GitLab, Gitea, and JType Kanban are Project Plugins. A project has at
most one installation of each provider. An installation is a project-shared
runtime identity and is not revoked when the user who installed it later leaves
the project.

Git repositories can only be attached to a Service through an enabled Git
Plugin. Raw Git URL Services are not part of the Plugin platform. One Git
installation can back many Services and each Service binds one repository.

A JType installation binds one workspace. Projects can browse all boards in the
workspace without creating an Automation. Kanban is enabled from the current
Service header: the user chooses one board once, then card creation or movement
into the conventional `ai` column starts a task for that Service. Results are
written back as card comments and, when available, the card moves to `done`.

SCM and Cron are the public Automation types. Internally the Service Kanban
binding reuses the strongly typed Kanban aggregate, claims, poller, and
writeback records; it is never listed or edited as an Automation.
SCM writes are never performed automatically by jcloud. jcode decides whether
to use the installed Skill and CLI to comment, push, open a PR or MR, or perform
another provider action. JType retains its existing result comment and optional
move to the completion column.

## Cluster configuration

The cluster stores one configuration for each Provider. GitHub is fixed to
github.com. GitLab, Gitea, and JType each support one configured instance URL.
GitHub, GitLab, and Gitea have independent login and Plugin switches. JType is
not a login provider.

Provider client secrets, GitHub App private keys, webhook secrets, access
tokens, and refresh tokens are encrypted in PostgreSQL with
`JCLOUD_MASTER_KEY`. The Kubernetes deployment retains only database access and
this master key. `AUTH_TOKEN_KEY` is not a supported alias after the clean-cut
release.

An empty cluster redirects to `/setup`. Setup records the public Cloud URL and
at least one working login Provider. The first successfully authenticated user
becomes Cluster Admin. There is intentionally no bootstrap token. Operators
must finish setup before exposing the cluster to an untrusted network.
Unauthenticated setup is rejected when any user already exists; an inconsistent
upgrade or restore requires database-operator recovery and cannot be claimed by
a new first visitor.

Changing a Provider URL or identity increments its configuration revision and
moves related installations to `action_required`. Existing runs continue. New
dependent runs and Automation dispatches stop until an Owner or Cluster Admin
reconnects and accepts the current Consent.

Database-backed OAuth state is signed and bound to the configuration revision,
canonical Provider origin, and client ID that generated the authorize URL. A
callback after any of those values changes fails with
`oauth_config_changed`; the user must start the flow again. Changing a
GitLab/Gitea origin, a JType base URL, or any OAuth client ID requires a new
client secret in the same Provider update. Changing the GitHub App ID similarly
requires a new private key.

## Installation and Consent

Only a Project Owner or Cluster Admin can connect, reconnect, enable, disable,
or uninstall a Plugin. Owners and Members can use an enabled installation to
create Services, Automations, and tasks. Viewers have read-only access.

Before leaving jcloud for a Provider authorization flow, the user confirms a
versioned Consent modal that states:

- the Provider instance and external identity;
- the actual scopes requested;
- that the credential is shared with the Project;
- that all healthy Project Plugins are mounted into all new Project tasks;
- that the credential remains active when the installer leaves the Project;
- that an `@jcode` Automation on a public repository lets any commenter start a
  task carrying all healthy Project Plugin credentials.

GitHub uses a cluster GitHub App. The user explicitly chooses an existing App
Installation or visits GitHub to install or change its repository selection.
After selection, Cloud mints a short-lived Installation token only in the
control plane and shows the token response's actual permissions and repository
selection. The user must acknowledge that exact preview; the final selection
request carries a digest that Cloud recomputes, so a permission change forces a
new preview instead of silently widening Consent.
The same App Installation can be linked to multiple Cloud Projects, but jcloud
never reuses it silently. GitLab, Gitea, and JType grants are independent for
each Project.

Requested permissions cover repository content and branches, pull or merge
requests, issues and comments, checks or pipelines, tags, and releases.
Organization administration, membership administration, OAuth application
administration, and repository deletion are excluded where the Provider offers
granular permissions. A coarse Provider scope can technically grant more than
jcloud needs and must be disclosed in Consent.

## UI information architecture

Project Settings has one Plugins surface with four fixed cards in a two-column
desktop grid and one column on narrow screens:

```text
GitHub                 GitLab
status and identity    status and identity
Services and triggers  Services and triggers

Gitea                  JType Kanban
status and identity    status and workspace
Services and triggers  boards and triggers
```

Cards use the canonical GitHub Mark, GitLab Tanuki, Gitea tea cup, and existing
JType Kanban logo. A card opens an independent detail route. The detail route
shows identity, health, granted scopes, resources, Services, Automation
summaries, audit events, and permission-aware actions.

Installation statuses are `connecting`, `enabled`, `disabled`,
`action_required`, `uninstalling`, and `error`. The unconnected card is a UI
projection, not a stored installation status.

Automations have a list route and an independent create/edit route for SCM and
Cron. The editor
contains Trigger, Filters, and Task sections. It has no SCM result writeback
section. Provider capability gaps are disabled with an explanation instead of
being hidden.

The active Service header has a right-aligned Kanban action whenever the JType
Plugin is healthy or that Service already has a binding. First use selects a
board and enables the binding; later use opens the real server-proxied board.
Disable removes the binding. A Project can bind a JType board to only one
Service, preventing one card from launching duplicate tasks against different
repositories. Disable keeps the binding and outstanding claims but stops new
polling, so a run already in progress can still write back from its frozen
snapshot.

## Service Kanban contract

`GET|PUT|DELETE /api/v1/services/{id}/kanban` is the only mutation surface.
Owner and Member may enable or disable it; Viewer is read-only. `PUT` accepts
the enabled Project JType Installation and the selected board's stable `b_…`
ID. The server supplies `trigger_column=ai` and `done_column=done`; the browser
does not offer per-Service column policy.

The board embed remains member-only because it is a read/write component. Its
document proxy accepts only the workspace of the healthy, enabled Project JType
Plugin, independent of whether a Service trigger has been enabled. Reads may
therefore browse the granted workspace before a trigger exists. Writes require
an enabled Service board and are restricted to parsed card frontmatter under
`cards/*.md`; arbitrary Markdown and foreign-board cards are rejected. Upstream
error bodies and any response reflecting the server-held bearer token are never
forwarded. Legacy `kanban_links` credentials are not a fallback.

The UI follows the existing Cloud tokens, typography, component library, dark
mode, focus treatment, and density. It adds no new design system and no
decorative motion.

## Automation contract

The normalized event catalog is:

| Family | Actions |
| --- | --- |
| `push` | `updated` |
| `pull_request` | `opened`, `reopened`, `synchronized`, `ready`, `closed`, `merged` |
| `review` | `approved`, `changes_requested`, `commented`, `dismissed`, `approval_removed` |
| `comment` | `created` |
| `issue` | `opened`, `reopened`, `updated`, `closed` |
| `check` | `completed` |
| `tag` | `created`, `deleted` |
| `release` | `published`, `updated`, `deleted` |

Label, assignee, and milestone changes are not separate first-release actions.
Unsupported Provider actions remain visible but unavailable.

The database rejects overlapping SCM Automations using the tuple Service,
event family, and action. One Automation may select several actions, but no
other Automation on the Service may claim any selected tuple.

Push defaults to the Service default branch and all paths. Check defaults to a
failure conclusion. Prompt templates are event-specific, editable, and limited
to documented variables.

An `@jcode` comment triggers only on creation. Matching is case-insensitive and
requires mention-token boundaries, so strings such as `@jcodeevil` and email
addresses do not trigger. The complete comment is passed to jcode.
Edits and deletions do not trigger. Each comment produces at most one run.
Provider stable account IDs are matched to linked Cloud identities when
possible; otherwise the run displays an external actor. Actor mapping does not
change task permissions.

Comments always run individually. Push, pull request synchronization, and check
completion use one running event plus the newest queued event for the same
Service and ref or object. Replaced queued events are marked `superseded`.
The supersede-and-create operation is atomic at the Store boundary: PostgreSQL
serializes a stable Automation + Service + ref/object key with a
transaction-scoped advisory lock, while the memory Store uses one mutex. The
last successfully committed delivery is the sole queued Run; if its insert
fails, the previous queued Run remains unchanged.

jcode-generated branches, actors, correlation markers, and comments are ignored
by default. Automation authors may disable that filter.

## Webhook and audit contract

Webhook authentication and a 1 MiB request limit are applied before
normalization. The provider payload is decoded once and discarded. jcloud does
not store raw bodies or request headers.

`webhook_receipts` stores only the delivery ID, an authenticated-payload digest
where the Provider signs the body, normalized family and action, external
actor, object reference, matching outcome, and sanitized error. It never stores
the raw payload. Delivery IDs are unique per Provider. For GitHub and Gitea,
the digest also rejects an authenticated-body replay whose unsigned delivery
header was changed. Rows expire after 30 days and are deleted in bounded,
multi-replica-safe batches. jcloud performs no automatic processing retry and
exposes no Replay action.

GitHub receives events through its cluster App webhook. GitLab and Gitea create
a repository webhook when the first SCM Automation for a Service is enabled and
remove it when the last one is disabled or deleted. Each GitLab/Gitea Service
binding owns an opaque `/webhooks/{provider}/{hook_id}` URL and an independent
random encrypted secret. Ingress resolves that URL to exactly one Service,
authenticates with the binding secret, and rejects the event unless its
Provider repository stable ID matches the immutable Service repository
binding. Provider configuration contains no shared GitLab/Gitea webhook secret.

Automation database changes commit before the external hook is reconciled. A
Provider failure returns `webhook_reconcile_failed` but does not roll the
Automation back: `automations_v2.last_error` and
`webhook_bindings.last_error` retain sanitized retry guidance, and saving or
re-enabling the Automation retries the operation. This is an intentional
visible-failure boundary, not an atomic cross-system transaction.

Consent, configuration, connection, status, uninstall, forced cleanup, and
master-key operations create immutable audit records.

## Runtime credentials and tools

At dispatch, jcloud snapshots all healthy, enabled installations belonging to
the Project. A snapshot stores references to immutable Provider configuration
and encrypted grant versions, rather than copying secrets into a per-run row.
A single database claim locks and revalidates those versions, writes the
snapshot references and runner-token hash, and moves the run to `scheduling`
before Kubernetes can create the Job. Provider disablement or uninstall cannot
cross that boundary ambiguously: it either wins first and blocks the claim, or
waits until the run is durably classified as already started.
A run-scoped endpoint returns only short-lived access material for that
snapshot. A sync companion periodically refreshes it and atomically
replaces files in a shared tmpfs volume. A one-shot initializer prevents the
runner from observing empty configuration; the runner's completion marker
terminates the companion so Kubernetes 1.28 Jobs can finish.

The runner never receives the GitHub App private key, OAuth refresh tokens, or
`JCLOUD_MASTER_KEY`. A run started before a Plugin is disabled can refresh the
installations in its immutable snapshot until the run terminates. New runs do
not snapshot disabled or unhealthy installations. A Service that depends on an
unavailable Plugin is blocked visibly; unrelated Services can still run.
Provider URL/config changes and reconnects append new live versions; existing
snapshots continue resolving their launch versions. Refresh-token rotation is
persisted only within the frozen grant version and can never overwrite a later
reconnect's version.
Historical Provider configuration and grant versions are retained while a live
Installation, a non-terminal Run, or a terminal JType Run with an unfinished
Kanban writeback can still use them. Bounded reconciliation reclaims
unreferenced encrypted rows only after the final runtime/writeback reference
ends. Terminal `run_plugin_snapshots` then remain as immutable audit records
containing only Provider, Installation, configuration-revision, and
credential-version identifiers; they do not retain historical ciphertext.
Kanban claims additionally freeze the workspace and completion column at
dispatch. Result comments and the optional move use that claim plus the run's
immutable JType Provider/grant snapshot, never the Installation or Provider
configuration that happens to be current when the run finishes. Once a claim
owns a run it is intentionally independent of the mutable Automation aggregate,
so disabling, deduplicating, or uninstalling configuration cannot erase the
pending writeback target.
Every RUN_TOKEN endpoint rejects queued and terminal runs, including the model
proxy and artifact/event inputs. Uninstalling an unrelated snapshotted Plugin
removes its managed CLI/Git files on the next successful sync without blocking
the remaining credentials.

Provider Services clone their immutable binding directly. The Git credential
helper reads access material from tmpfs; the repository URL and environment
never contain the token. Cloud has no source-bundle endpoint, branch-bundle
upload endpoint, automatic push, PR/MR creation, or SCM comment pass.

Runner Jobs execute as fixed UID/GID `10001`, never mount a ServiceAccount
token, disable privilege escalation, drop every Linux capability, and use the
runtime-default seccomp profile. The Pod fsGroup makes PVC and tmpfs mounts
writable without root.

The release-pinned Orchestrator image owns the canonical Plugin runtime bundle:

- GitHub built-in jcode skills and `gh`, without a GitHub MCP server;
- a GitLab skill and `glab`, without a GitLab MCP server;
- a jcloud-owned Gitea skill and official `tea`, without a Gitea MCP server;
- the JType MCP adapter contract, without a JType skill;
- the credential writer for Git clone, fetch, and push.

At dispatch, an Orchestrator-owned init container copies only the CLI and Skill
assets named by the immutable `RunPluginSnapshot` into a memory-backed runtime
volume. GitHub, GitLab, and Gitea receive their matching CLI and Skill; JType
receives MCP configuration only. The generic Runner image contains no Provider
CLI, Skill, command, or credential-format implementation. It mounts the runtime
and credential volumes read-only. Console controls installation enablement;
Orchestrator is the sole authority that translates the resulting run snapshot
into injected assets. Missing or unknown runtime assets fail the run closed.
Each managed Skill is mounted at its own Provider directory, preserving
unrelated Skills already present in a persistent task HOME.
The prewarm DaemonSet pins both the generic Runner and Orchestrator Plugin
runtime images on every task node so runtime injection does not add a cold pull.

## Disable and uninstall

Disable keeps the installation, Services, and Automations, but stops new
dependent work. Running tasks continue from their snapshots.

Uninstall is destructive. The UI first displays counts and names of affected
Services and Automations and requires typed confirmation. Uninstall then removes
Provider hooks, all dependent Automations and Services, and the installation
credential. Audit events remain. External cleanup failure leaves the
installation in `uninstalling`; Owner or Cluster Admin can force local deletion
after accepting an orphan-hook warning.

## Clean-cut migration and release

Migration `0043_plugin_platform.sql` clears legacy integrations, Kanban links,
Schedules, Automations, Runs, Services, webhook bindings, and their dependent
test data while preserving users, Projects, memberships, model configuration,
devices, and API keys. It creates the Plugin aggregates and typed trigger
tables. Append-only migration `0044_plugin_store_integrity.sql` adds database
guards for same-project/same-Provider Service bindings and for exactly-one,
correctly typed Automation trigger aggregates. Append-only migration `0046`
deterministically keeps the oldest historical Kanban aggregate for each Service
and board, freezes all existing claim targets, preserves run-bound claims,
removes duplicate aggregates, then adds one-Kanban-per-Service and
one-Service-per-board uniqueness. It also detaches frozen writeback claims from
the mutable Automation foreign key. Append-only migration `0047` records
Automation-origin Run identity; `0048` adds branch/effort/goal inputs;
`0049`/`0050` add attachment stages plus retry/resume bindings; `0051` replaces
shared self-hosted webhook credentials with per-Service opaque routes and
encrypted secrets; and `0052` allows historical ciphertext versions to be
reclaimed while snapshot audit IDs remain. `0053` repairs attachment upload
state for databases that had already recorded an earlier form of `0049`; `0054`
persists the SCM coalescing key and enforces at most one queued Run for that key.
There is no legacy API compatibility period.

Orchestrator, Console, runner image, and migration ship in one release. The
database is backed up first. A failed migration keeps readiness unhealthy and
must not allow a mixed Console and Orchestrator version.

Supported baselines are github.com, GitLab 17.11+, Gitea 1.25+, and JType 0.2+.
Lower self-hosted versions can connect if their basic APIs work, but capability
probing disables unsupported triggers.

## Provider trust boundary

A Cluster Admin configures each Provider instance as a trusted credential
recipient and content authority. Cloud never serializes a credential from its
own state, redacts upstream error bodies, and rejects common direct, JSON,
Base64, URL, and hex reflections as defense in depth. A malicious Provider that
has already received a valid bearer token can nevertheless encode that token
inside otherwise valid repository, issue, or board content; no transparent
content proxy can detect every reversible encoding while still returning
Provider data. Only Provider instances controlled by a trusted operator may be
configured.

## Accepted security exceptions

The following are product decisions, not implementation omissions:

1. Any public repository commenter can cause an `@jcode` Automation to launch a
   task with all healthy Project Plugin credentials.
2. There is no `@jcode`-specific rate limit or per-Automation confirmation.
3. Every Project task receives all healthy Project Plugin credentials.
4. The first visitor can configure an uninitialized cluster.
5. Coarse GitLab or Gitea OAuth scopes may exceed the intended feature envelope.
6. Webhook processing has no raw-payload archive, automatic retry, or Replay.
7. Loss of every login Provider requires direct database recovery.

Security review must report these risks clearly. It must not silently change
the accepted behavior. A reviewer may still mark them as a release blocker for
an explicit product decision.

## Completion tranche: provider operations and task composition

Status: approved for implementation on 2026-07-26.

This tranche closes the remaining operational gaps discovered during the first
production exercise. An item is complete only when its durable behavior,
failure state, tests, operator documentation, and production verification are
all present. A feature that exists only in a fixture or static capability table
is not considered production-verified.

### Receipt retention

`webhook_receipts.expires_at` is an enforced retention boundary, not metadata.
The Orchestrator periodically deletes expired receipts in bounded batches.
Cleanup is idempotent, observable, safe under multiple replicas, and never
deletes unexpired rows. PostgreSQL and memory stores share the same contract.
The cleanup loop records failures without making the API unready and retries on
the next interval.

### Provider probing and capability degradation

“Test connection” performs the strongest provider-specific cluster probe that
can be proven without inventing an end-user grant:

- GitHub validates the App identity and App credentials, while OAuth identity
  and Installation access remain separately visible;
- GitLab and Gitea validate instance reachability and read the server version
  when the instance exposes it; their OAuth client and Project grant are proven
  by the real authorization and resource-discovery flow;
- JType validates its public health endpoint; its Project grant and server
  access are proven by workspace discovery and MCP initialization. Cloud does
  not invent a JType version when the instance exposes no version endpoint.

The probe persists the observed version, capability revision, timestamp, and a
sanitized health error. It must not persist or return probe credentials.
Provider capability responses combine the normalized event catalog with the
observed Provider version and API feature evidence. Unsupported actions remain
visible but disabled with a reason. A reachability-only response must not be
presented as a successful authenticated connection test.

### SCM verification and event presentation

The production verification matrix includes a real GitHub push and a newly
created `@jcode` comment after a model is configured. Both must create Runs with
the correct Automation origin, complete comment prompt, actor attribution,
delivery deduplication, and no Cloud-generated SCM writeback.

The full normalized catalog remains supported and testable. The Automation UI
promotes the common actions—push, pull request open/update/merge, `@jcode`
comment, issue open/update, and check completion—and places less common review,
tag, release, reopen, close, and deletion actions under an explicit “More
events” disclosure. Collapsing the disclosure never clears selected actions.
Provider capability gaps remain visible and explained.

GitLab and Gitea are complete only after a configured-instance journey covers
OAuth, Project Installation, repository selection, Service clone, webhook
create/delete, normalized dispatch, CLI use, disable/reconnect, and uninstall.
When an external instance is unavailable, these journeys remain a documented
production-verification dependency rather than being reported as passing.

### Runtime Skills and CLIs

Runtime assets are injected at Run start by Console/Orchestrator policy and the
immutable Plugin snapshot. They are never compiled into the generic Runner.
Verification executes the matching Skill and CLI (`gh`, `glab`, or `tea`) in a
real task, exercises Git credential-helper fetch/push, and proves that disabling
a Plugin removes its assets from new Runs without altering already-started
Runs. JType continues to receive MCP configuration and no Skill.

### Task composition

Manual task creation uses one task-options contract. SCM-triggered Runs derive
their branch from the normalized event, but Automation templates do not accept
manual attachment stages:

- **Branch:** a Service repository branch can be selected. Manual tasks clone
  the selected branch. SCM-triggered Runs use the event ref/object branch when
  present and otherwise the configured Automation branch filter or Service
  default branch. The selected branch is validated against the live Provider
  catalog and persisted on the Run for audit. The current contract stores the
  branch name, not the observed commit SHA: if the branch advances between Run
  creation and clone, the Job checks out the newer ref. Commit pinning remains
  a known P2 consistency follow-up.
- **Model effort:** when the selected model supports reasoning effort, the user
  can choose an allowed effort or `auto`. The Orchestrator validates the choice
  against model capabilities and passes the resolved value to jcode at Run
  start. Unsupported values fail visibly.
- **Goal mode:** a task may start in goal mode with a concrete goal statement.
  Goal state is initialized through the
  task startup contract and remains observable in the task event stream.
- **Attachments:** a user can attach files before dispatch. Uploads use the
  existing object-storage security boundary through a Cloud proxy. Each file is
  at most 25 MiB; a Run accepts at most 10 files and 100 MiB total. One
  Project/user may hold at most 20 unexpired stages and 250 MiB of staged data.
  Upload intents expire after 10 minutes and move atomically through
  `pending → uploading → uploaded`; the declared and received lengths must
  match before a Run can consume the stage. Stages are bound to the creating
  Project and user, then transactionally bound to the target Run. User
  filenames are display metadata only and cannot choose the object key or
  filesystem path. Init containers verify each download length, write an
  opaque-path manifest, and the task mounts the attachment tmpfs read-only.
  Attachment bytes plus manifest overhead are added to the Pod memory request,
  limit, and tmpfs size. Retry and resume reuse immutable bindings;
  Automation-created Runs do not inherit unrelated manual-task attachments.

All four options must survive API validation, persistence, dispatch retries,
and task rendering. The Console disables unavailable choices with an actionable
explanation instead of silently dropping them.

### Console cleanup and localization

The obsolete Git Integration, Project Kanban-link, paste-a-PAT, and legacy
Project Settings modal branches are removed together with unreachable hooks,
types, mocks, and tests. Current Plugin and Service Kanban surfaces remain the
only supported paths.

Every user-facing Plugin detail string uses the locale catalog, including
health errors, resource failures, consent metadata, stable identity labels,
workspace binding, Service and Automation empty states, audit history, and
trigger names. Provider identifiers, scope identifiers, repository names, and
external error codes remain untranslated data.

### Verification gates

The tranche requires:

1. PostgreSQL and memory-store retention tests, including concurrent cleanup.
2. Provider probe fixtures for success, invalid credentials, unsupported
   versions, malformed version responses, timeouts, and secret redaction.
3. Webhook normalization, capability filtering, event disclosure, branch
   filtering, deduplication, and coalescing tests.
4. Task option API/store/dispatch tests for branch, effort, goal, and
   attachments, including authorization and path traversal cases.
5. Runtime snapshot tests proving correct assets and removal on disable.
6. Console unit, accessibility, localization, typecheck, and production build.
7. Full Orchestrator tests and PostgreSQL-gated store tests.
8. Independent domain/data-consistency and attacker-perspective reviews before
   release.
9. A database backup before applying the release migration and a production
   smoke test after rollout.
