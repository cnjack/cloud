# jcode Cloud interface prototypes

These files are visual and interaction references. They are not runtime assets,
and the sample records in them are never a source of product data.

## Page map

Each product page or major state has its own HTML document. Shared visual rules,
icons, and small prototype-only interactions live under `assets/`.

| Prototype | Product surface |
| --- | --- |
| `index.html` | Prototype directory only |
| `projects.html` | Project list, populated state |
| `projects-empty.html` | First-run Project list empty state |
| `new-project.html` | Create Project editor |
| `cluster-overview.html` | Cluster capacity, guardrails, runner, and version |
| `cluster-models.html` | Cluster model catalog and Project grants |
| `cluster-connections.html` | jtype, Git provider policy, and authentication wiring |
| `cluster-access-denied.html` | Direct Cluster route for a non-cluster-admin |
| `sign-in.html` | OAuth-first sign in with console-token fallback |
| `setup.html` | Orchestrator unreachable state |
| `welcome.html` | First-user / cluster-admin welcome state |
| `not-found.html` | Routed 404 state |
| `project-workspace.html` | Existing Project and conversation reference |

## Layout contract

- Authenticated global routes use the same viewport shell as the accepted
  Project workspace: a warm background, contextual rail, and one inset white
  work surface.
- The global rail contains only real navigation: Projects, Cluster, and recent
  Project shortcuts. It does not invent service health.
- Sign-in and setup cannot use authenticated navigation, but preserve the same
  rail-and-surface composition.
- Orange is reserved for the primary action and focus. Status colors only
  describe explicitly labelled sample fixture data.
- The application mark and model-provider icons come from the canonical jcode
  assets. Provider rows use the same icon mapping and compact hierarchy as
  jcode instead of invented initials.
- Every unavailable dependency remains visible and explains the next action.
- Desktop has one surface scroll owner. At narrow widths, the rail collapses to
  a compact top navigation and the document becomes the scroll owner.

## Implementation notes

The designs deliberately stay within current backend contracts:

- Project search is client-side over the existing Project list response.
- Project creation still sends only a name, then navigates to the Project to
  connect Services. `new-project.html` proposes a route-owned editor instead of
  the current modal; implementing it needs a Console route, not a new API.
- Cluster sections split the current `/system` information architecture. They
  can use a `section` query parameter or nested client routes. Capacity,
  guardrails, models, grants, jtype configuration, provider policy, auth counts,
  runner, and version already have Console queries.
- Provider credentials remain write-only. The UI may show whether a credential
  is configured, never its stored value.
- `cluster-models.html` groups models under their provider, matching jcode's
  picker and provider-management hierarchy while retaining Cloud-specific
  Project grants. The custom Coding Plan fixture visibly disables catalog
  browsing because that endpoint does not advertise one.
- Auth, setup, welcome, and 404 are visual restructures over existing states.

## Shared asset sources

- `assets/app-icon.svg` embeds the canonical 128px jcode desktop application
  icon so the static prototype remains portable.
- `assets/provider-zhipu.svg`, `assets/provider-qwen.svg`, and
  `assets/provider-openai.svg` mirror the provider icon assets consumed by
  jcode's `ProviderIcon` mapping.

## Review checklist

1. Open each HTML file directly or serve `design/` from a local static server.
2. Check at 1440×900 and 390×844.
3. Verify keyboard focus, active navigation, project search, theme toggle,
   setup-command copy, and visible prototype feedback.
4. Treat `project-workspace.html` as the baseline for spacing and density; do
   not merge the new pages back into it.
