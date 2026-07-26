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
workspace without creating a trigger. A Kanban Automation chooses its board,
trigger column, optional completion column, and target Service.

SCM, Kanban, and Cron use one Automation aggregate with typed trigger records.
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

Automations have a list route and an independent create/edit route. The editor
contains Trigger, Filters, and Task sections. It has no SCM result writeback
section. Provider capability gaps are disabled with an explanation instead of
being hidden.

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

jcode-generated branches, actors, correlation markers, and comments are ignored
by default. Automation authors may disable that filter.

## Webhook and audit contract

Webhook authentication and a 1 MiB request limit are applied before
normalization. The provider payload is decoded once and discarded. jcloud does
not store raw bodies or request headers.

`webhook_receipts` stores only the delivery ID, normalized family and action,
external actor, object reference, matching outcome, and sanitized error. Rows
expire after 30 days. Delivery IDs are unique per Provider. jcloud performs no
automatic processing retry and exposes no Replay action.

GitHub receives events through its cluster App webhook. GitLab and Gitea create
a repository webhook when the first SCM Automation for a Service is enabled and
remove it when the last one is disabled or deleted.

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
correctly typed Automation trigger aggregates. There is no legacy API
compatibility period.

Orchestrator, Console, runner image, and migration ship in one release. The
database is backed up first. A failed migration keeps readiness unhealthy and
must not allow a mixed Console and Orchestrator version.

Supported baselines are github.com, GitLab 17.11+, Gitea 1.25+, and JType 0.2+.
Lower self-hosted versions can connect if their basic APIs work, but capability
probing disables unsupported triggers.

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
