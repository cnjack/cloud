# 15 · Project Workspace Architecture

> Status: accepted implementation architecture (2026-07-12).
>
> This document turns `design/project-workspace.html` into a buildable Console
> architecture. The HTML file remains a visual and interaction reference; it is
> not a runtime asset and must never be copied into the product as a fake-data
> screen.

## 0. Problem

The first Project workspace implementation adopted a service rail, a chat-like
composer, Automations, and Kanban entry points, but retained the old page shell
and generic page primitives. The result is structurally different from the
reference:

| Reference intent | Current implementation | Required change |
| --- | --- | --- |
| A Project is the complete working surface. | `AppShell` owns a global top bar and `ProjectDetailPage` adds a second rail below it. | Give project routes a route-scoped `ProjectWorkspaceShell`; hide the global top bar for that route instead of competing with the workspace. |
| A selected Service is the execution context. | The composer also exposes a repository select, duplicating the rail's selection. | The rail is the only service selector. Its selection is URL-addressable. |
| Tasks, Automations, and Settings are peer workspace modes. | Tasks and Automations are local state; Settings is a modal action. | Make all three modes addressable workspace tabs. Keep the advanced project-admin dialog as a nested action, not the primary information architecture. |
| The composer is a single focused task surface. | An owner-only service-default editor appears above the input. | Keep per-run model and permission controls in the composer; move service default-model editing to Settings. |
| Recent work is a readable activity feed. | Runs are rendered as a four-column generic table. | Use semantic activity rows with task, context, state, and time, while preserving run links and filters. |
| Provider/event status is trustworthy. | The visual reference contains illustrative webhook health. | Preserve the existing provider `@jcode review` paths, but render webhook registration and delivery health as explicitly unavailable when the API cannot verify them. |

This is an information-architecture problem first. Token tuning alone cannot
repair it.

## 1. Product boundaries

### What this change owns

- Project route shell, service navigation, task composer, activity list,
  Automations surface, and the Settings entry surface.
- Deep-linkable active service and workspace tab state.
- One scroll owner for the page surface.
- Visible status for every dependency that the existing API can verify.

### What this change does not invent

- Webhook registration or delivery health. The current API exposes neither.
- Automatic PR-event review beyond the existing provider `@jcode review`
  webhook paths.
- Attachments, voice input, or arbitrary file context controls.
- A client-side fallback model or a simulated successful integration.

When a requested interaction needs one of these capabilities, the UI must say
that it is unavailable and identify the owner/configuration path. It must not
draw a green health state from a prototype fixture.

## 2. Route and state model

The URL owns durable workspace navigation:

```
/projects/:projectId?service=:serviceId&tab=tasks|automations|settings
```

- Invalid or absent `service` resolves to the project's `default` service, then
  the first service. It is normalized with `replace`, so copied URLs are stable.
- Invalid or absent `tab` resolves to `tasks`.
- A Project route transition discards the prior project's transient task draft,
  selected per-run model, permission mode, and add-service form state.
- The active service change preserves the selected tab. A tab change resets only
  the workspace content scroll position.

This avoids hidden local selection state, restores browser navigation, and
makes a pasted Project URL describe one unambiguous surface.

## 3. Component boundaries

```
AppShell (non-Project app chrome)
└─ ProjectWorkspaceShell (only on /projects/:projectId)
   ├─ ProjectRail
   │  ├─ ProjectSummary
   │  ├─ ServiceNavigation
   │  └─ ClusterFooter
   ├─ WorkspaceUtilityBar
   │  └─ KanbanEntry / visible unavailable state / identity
   └─ ServiceWorkspace
      ├─ ServiceHeader
      ├─ WorkspaceTabs
      └─ WorkspaceScrollRegion
         ├─ TasksPanel
         │  ├─ TaskComposer
         │  └─ RunActivityList
         ├─ AutomationsPanel
         └─ SettingsPanel
            ├─ ServiceModelPolicy
            └─ ProjectAdministrationEntry → ProjectSettingsModal
```

`ProjectDetailPage` is the data/controller boundary: it loads the Project,
runs, model gate, integrations, and Kanban links, derives a small workspace
view model, and coordinates mutations. Presentational children do not fetch
or mutate project-wide state by themselves.

`TaskComposer` owns only a transient `TaskDraft`:

```ts
interface TaskDraft {
  prompt: string;
  modelId: string;       // empty means service default
  permissionMode: '' | 'approval';
}
```

`SettingsPanel` owns the service default-model editor. The existing
`ProjectSettingsModal` retains advanced project administration (members,
integrations, Kanban configuration, and API keys) behind an explicit entry.

## 4. Capability model

The workspace receives capability state rather than inferring success from the
service provider name:

| Capability | Existing source | UI behavior |
| --- | --- | --- |
| Dispatch a session | Project role + model gate | Show composer to member/owner; disable it with the model-gate explanation when unavailable. |
| Choose a per-run model | `listProjectModels` | Show only granted models; no model list means environment fallback, not a made-up model. |
| Change a service default model | owner + granted models + `updateService` | Expose in Settings only. |
| Schedule work | `listServiceSchedules` and schedule mutations | Render in Automations. |
| Provider review event health | no API contract | Keep existing provider review paths visible, but render health as "status unavailable"; do not claim a hook is healthy. |
| Kanban | `listProjectBoardLinks` | Show the real board entry when links load; show a retryable unavailable state if the query fails. |

## 5. Layout and scroll contract

- Desktop Project routes use one viewport-sized workspace shell. The only
  vertical scroll owner is `WorkspaceScrollRegion`; the rail has its own
  independent overflow for long service lists.
- The shell and every flex/grid ancestor of `WorkspaceScrollRegion` has
  `min-height: 0`. Modal overlays remain outside this scroll owner.
- At narrow widths, the rail becomes a horizontally scrollable service strip;
  the active context remains visible and the content has the normal document
  scroll. This is a responsive layout change, not a second nested scroller.
- The visual language is restrained jcode desktop UI: warm off-white surface,
  hairline borders, compact monospace metadata, a single orange action accent,
  and no decorative status that does not come from real data.

## 6. Implementation sequence

1. Add URL-state helpers and tests for normalization, route transitions, and
   tab/service changes.
2. Extract the workspace shell and move rail/header/utility controls into it;
   make `AppShell` route-aware so it does not duplicate project chrome.
3. Extract `TaskComposer`, `RunActivityList`, and `SettingsPanel`; remove the
   default-model control and repository select from the composer.
4. Replace the run table with activity rows without changing run navigation,
   filters, role gating, or API mutations.
5. Make Automations and Kanban capability states explicit, then run the Console
   test, typecheck, and visual QA pass.

## 7. Test design

The following tests are written or updated before each matching implementation
step:

| Case | Assertion |
| --- | --- |
| URL state | An invalid tab/service normalizes to a valid service and `tasks`; a valid selected service survives refresh. |
| Service context | Choosing a rail item changes the URL and dispatches the composer against that service; the composer never has a second repository picker. |
| Tab behavior | Tasks, Automations, and Settings are ARIA tabs; tab changes reset only the content scroll; service changes preserve the active tab. |
| Model scope | Per-run model selection stays in the composer; only an owner sees the service default-model control in Settings. |
| Fail-visible gates | Missing model disables dispatch with the existing remediation; failed Kanban lookup stays visible; no test fixture renders a positive webhook health state. |
| Activity | A run row links to the existing run detail route and still exposes kind, status, retry provenance, and timestamp. |
| Role gates | Viewer cannot compose or administer; member can run and see only allowed actions; owner can open project administration. |
| Scroll | The workspace scroll surface resets on a tab change and all internal desktop flex/grid parents can shrink. |

## 8. Acceptance criteria

- The rendered Project route has one coherent workspace chrome, not a global
  dashboard above a second workspace.
- The rail is the only active-service selector.
- Settings is a first-class workspace mode; service model policy no longer
  displaces the task input.
- Recent tasks read as activity rows rather than an administrative table.
- Kanban remains the real server-proxied board and all unavailable integrations
  remain visibly unavailable.
- The implementation has no dependency on static data in `design/` at runtime.
