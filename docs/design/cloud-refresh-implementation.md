# Unified Cloud implementation and acceptance

Approved visual/interaction contract: `design/work-home-refresh.html` and
`design/cloud-suite.md` with its linked page-scoped prototypes.

## Test-first acceptance matrix

| Requirement | Evidence required | Status |
| --- | --- | --- |
| Consistent shell on task, board, reviews, automation, usage, settings and devices | Route tests plus desktop/mobile browser screenshots | Verified publicly at desktop/mobile sizes; automation repository header and Board recovery link verified after v0.0.161 rollout |
| Account → Repository → Task navigation | No Project navigation, repository picker/search, history and back navigation tests | Verified: public repository picker, task routes and same-context Board; route regression tests pass |
| Composer context/drafts/model/branch/permission | Real submission payload and state retention tests; actionable dependency errors | Verified: public repository/model selection and Ask submission, real approval/follow-up/resume; automated draft/context cases pass |
| Search and status filtering | Combined filters and empty/no-match tests against real run records | Verified publicly with real QA run and combined no-match filter; Clear filters restores results |
| Board and automations | Existing jtype and automation API operations, errors and editing journeys | Public connected Board and automation editing/readback verified; existing owner/config blockers visible; API regression tests pass |
| Review detail, conversation, approvals, diff and follow-up | Real run/event API tests and authenticated UI journey | Verified: real public review c1e2fbd1e22549eef8b23daeab79bb30 and QA task journeys; encrypted remote diff/draft verified on local integration rig |
| Account profile and task defaults | User-scoped persistence, validation, authorization and consumer tests | API/UI regression cases passed; public profile/defaults/preferences render existing values without changing user settings |
| Personal model provider management | Encrypted credentials, owner isolation, API/UI tests, runtime resolution | Tests and local real authorization start passed; public ChatGPT start blocked by upstream unsupported_country_region_territory (403); completed personal authorization/inference unverified |
| Git connections and recovery | Existing real OAuth links, expired credential states | Existing github/cnjack connection and recovery UI verified; automated expired/rejected credential cases pass |
| Devices, workspace and session routes | Loading/error/empty/offline/pairing tests; encrypted content gated including title | Public online/offline devices, existing paired encrypted history and mobile routes verified; installed v0.13.5 correctly gates new inspector features pending device upgrade |
| Device authorization and pairing | Real existing pairing flow retained, no simulated actions | Real local encrypted pairing passed; public existing pairing works and setup/code route verified; no new security grant made during acceptance |
| Repository/account usage and cluster surfaces | Scope filters and API-backed data, responsive rendering | Public repository 8/8 captured requests, device-account usage empty state, cluster capacity/models/connections and 390px layout verified |
| Release | Full Console tests/typecheck, relevant Go/PG tests, exact CI source SHA | Verified: Console v0.0.163 at 60dc9f2; 603 tests, typecheck, build and CI passed; backend/Runner v0.0.159 retains the verified Go/PG and shared UI 151-test evidence |
| Deployment and acceptance | Immutable image/digest, migrations/readiness, authenticated public-path UI/API checks | Console v0.0.163 and backend/Runner v0.0.159 deployed and ready; public runtime, page/mobile navigation and HTTP headers verified; full ChatGPT authorization/inference externally blocked |

## Verified starting state

- Checkout started at `aa6d8f3` on `main`; only this task's design artifacts were
  uncommitted. Existing Console APIs and reusable relay components are retained.
- Company deployments currently pin Aliyun image versions, not `latest`:
  Console `v0.0.158`, Orchestrator `v0.0.156`. Publish and set the exact verified
  release version, then inspect pod image IDs; restarting alone is insufficient.
- Personal models currently expose read-only grants. The approved provider
  management/profile/preferences designs need additional account APIs and
  persistence. This is implementation work, not an approved scope reduction.
- Dedicated device list/workspace routes must replace legacy redirects while
  keeping owner authorization and client-side decryption intact.

The goal is implementation, deployment, and acceptance of the whole contract.
Do not mark complete on design screenshots, builds, or partial UI work alone.

## Implementation checkpoints (historical)

- Added the shared account rail to utility routes and dedicated `/devices`,
  `/devices/:id` workspaces. Remote session metadata and stream hooks now mount
  only after device identity loads and browser pairing succeeds. Offline paired
  sessions retain history with sending disabled.
- Added task search/status filtering, compact repository tabs and the shared
  settings/conversation canvas. Device authorization now returns to its workspace.
- Added `/api/v1/account/profile` for display name and private task/UI defaults,
  backed by `users.preferences` (migration 0077). This is distinct from the existing
  encrypted `/account/settings` mesh document, which is unchanged.
- Console regression: 572 tests passed after rebuilding shared UI at CI's
  `codex/shared-conversation` commit `895fd6bfa14f2226a4b5ca60889d01d37cb5b102`.
  The sibling working checkout is on a different branch; do not replace it.
  Orchestrator full Go suite also passed before the remaining provider changes.
- Remaining work includes personal provider ownership/CRUD/runtime access,
  new form/default-consumer coverage, responsive browser checks, deployment and
  authenticated public-path acceptance. This progress is not release acceptance.

### Personal model ownership contract

Before implementing provider management, test owner-only CRUD/catalog/verification,
write-only encrypted keys and headers, same-name providers across accounts, and
rejection through cluster/project grant paths. A provider gets an immutable
`owner_user_id`, mutually exclusive with `project_id`. Child model ownership is
derived from its provider rather than maintained as another mutable copy.
`ListModelsForAccount` includes the owner's enabled personal models plus direct
cluster grants; repository/automation/runtime selection must continue to use that
account authorization set. Project model lists never inherit personal credentials.

### Attachment and recovery acceptance cases

The approved paperclip accepts real files before the first task, including an
unmaterialized repository. Verify account OAuth ownership before materializing
the repository and reuse the existing bounded, Cloud-proxied upload contract.
Test missing object storage, unavailable repository, non-human principals,
completed upload stage ownership, and actual stage IDs in task submission.
Keep upload errors and rejected task prompts visible with retry; never silently
send a task without its selected files. Keep staged files scoped to their
repository when the user changes the composer context.

### Remote inspection and draft delivery extension

The remote-conversation prototype includes a Changes/Details inspector and draft
PR delivery. The Cloud API and shared encrypted client now route
`workspace.changes` and `workspace.draft_pr`. The matching device implementation
is isolated in the sibling worktree `/tmp/cloud-refresh-jcode-device` on branch
`codex/cloud-refresh-device`; the user's existing sibling checkout is untouched.
Local endpoints resolve the explicit session, never the active session fallback.
Older devices show an upgrade link, and offline devices retain the last loaded
preview while disabling refresh and delivery.

The delivery dialog binds the reviewed revision and explicitly selected files.
Device tests use real Git repositories and verify selected-only commit trees,
unchanged original workspace/index/HEAD, partial-push recovery, and retry reuse.
GitHub delivery uses the device's existing Git/GitHub CLI identity. A separate encrypted draft preview reads the complete diff against the current
GitHub default branch, including committed changes; publication binds that base
SHA and preview revision. Only selected patches enter the delivery commit. Actual
GitHub delivery acceptance, encrypted full-stack acceptance, and device
distribution still require review before the full contract is done.

Personal ChatGPT OAuth is now implemented alongside API Key management. The
real device-code start/cancel flow is verified locally; user-completed account
authorization and actual OAuth inference remain separate acceptance requirements.

### Additional verified progress

- Account navigation now combines authorized repositories consistently across
  home, settings, and task detail; failures are visible with retry.
- Identical upstream model names retain separate catalog/provider identity in
  task selection. Provider labels are public metadata, never credentials.
- Account profile/default forms saved and survived reload against a local real
  backend/database. Desktop/mobile browser checks covered home, profile, models,
  devices, guide, and a stored task (fixture has no conversation events).
- Attachments now materialize an authorized Account repository and reuse the
  bounded upload stage API. API tests prove stage consumption into a task;
  frontend tests prove failed upload blocking/retry and actual stage IDs.
- Query caches are replaced synchronously on principal changes; a regression
  test prevents prior-account private settings from flashing while loading.
- The full Console suite passed 581 tests before the latest recovery/identity
  changes. Subsequent complete verification is required before release.

Latest checkpoint: Console 583/583 tests passed, `pnpm typecheck` and production
`pnpm build` passed; full Orchestrator Go and real PostgreSQL store suites passed.
The new preference-consumer tests and latest connection-state copy need their
final focused rerun. These are local results, not production acceptance.


### Remote implementation checkpoint

Console: 591 tests passed, including inspector navigation, offline/upgrade
states, retry, and selected-file/revision delivery. Shared device UI: 149 tests
passed; typechecking passed. Full Cloud and sibling-device Go suites passed.
These are local checks; device release, authenticated public-path acceptance,
and model OAuth remain unfinished. No deployment has been performed yet.

### Authenticated local device acceptance

A freshly built device binary ran in `/tmp/cloud-refresh-device-home` with
`JCODE_CLOUD_SECRET_BACKEND=file`, against the real local Cloud API and PostgreSQL.
The isolated QA account authorized device login, and the browser completed the
real P-256 pairing flow through the CLI approval command. A deliberately seeded,
labelled session metadata fixture pointed at a real Git worktree; no LLM output
or task completion was simulated.

The browser verified that the private title was absent before pairing, then
opened the actual `hello.txt` patch (`before` → `after`) through the encrypted
Cloud command/ACK path. PostgreSQL confirmed both command and result carry an
`enc` envelope, with zero plaintext `hello.txt` matches. Desktop 1440×1000 and
mobile 390×844 screenshots had no page errors or horizontal overflow. Visual
review fixed native-button styling, interpolation syntax and empty-thread sizing.

This journey found and fixed a real pairing race: storing a CEK unmounts the
pairing card before it can invalidate ciphertext caches. Invalidation now occurs
even after that unmount; a regression test covers it. Local device inspection is
verified, but public deployment, real GitHub delivery, and model OAuth are not.

### Personal ChatGPT authorization contract and test cases

Implement the approved account authorization/recovery interaction as a real
ChatGPT device-code flow, following the sibling jcode provider-auth contract.
Public documentation describes the device-code user interaction; jcode's existing
adapter is the reference for the private upstream wire protocol. Credentials and
pending authorization secrets are encrypted in PostgreSQL, keyed by an immutable
personal provider. They are never returned to the browser or delivered to a
Runner. A database row lock serializes polling and rotating refresh tokens across
orchestrator replicas. The provider endpoint and authentication method are pinned;
reauthorizing an existing connection must preserve the same upstream account.

Test before delivery: owner/service-principal isolation; encrypted persistence and
restart; pending/slow polling/expiry/retry; refresh rotation under concurrent
requests; invalid-grant recovery; no upstream secret/error-body leakage; cascade
cleanup; explicit Responses protocol through the run proxy; selected model and
account headers retained; UI authorization cancellation/retry/reauthorization.
No successful authorization or real inference will be claimed without an actual
user-completed provider flow and runtime evidence.


### ChatGPT OAuth implementation checkpoint

Migration 0079 persists encrypted authorization state per personal provider. The
provider's endpoint/authentication method is pinned. PostgreSQL locking tests use
two independent connections and verify serialized credential updates plus
account-deletion cascade. API tests cover owner isolation, managed-field rejection,
model-toggle preservation of OAuth auth type, and actionable authorization errors.

The resolver refreshes OAuth inside the control plane; Runner/device configs carry
only a scoped proxy token and explicit `codex_responses` protocol. The proxy pins
the managed operation to Responses, injects the reviewed account identity, strips
browser cookies, and records rejected credentials without revoking a newer token.
The sibling jcode transport test verifies the actual HTTP request and streamed
response using an isolated upstream fixture.

The authenticated local browser obtained a real OpenAI device authorization code
(HTTP 200 pending), cancelled it, and deleted its QA provider (204). No personal
account was signed in and no OAuth model inference was claimed. Desktop/mobile
screenshots were inspected; the 390px viewport has no horizontal overflow or
page errors. Console 596 tests passed. Full Cloud Go, real PostgreSQL integration,
sibling full Go and `make lint` passed before subsequent final release checks.


### Live remote draft acceptance

The paired local browser opened a distinct session rooted at an isolated clone of
`cnjack/cloud`, read the real GitHub default-branch preview, explicitly selected
`cloud-refresh-acceptance.txt`, and created draft PR
`https://github.com/cnjack/cloud/pull/30`. GitHub readback confirmed draft status,
base `main`, one file and two added lines. The original checkout remained on
`main` at `aa6d8f3`, its staged diff stayed empty, and the fixture stayed untracked.
The acceptance PR was then closed without merging and its remote branch deleted.

PostgreSQL readback confirmed all preview/delivery commands and ACK results used
`enc: aes-256-gcm`, including the failed attempts, with zero plaintext fixture-name
matches. The QA device keeps an isolated jcode HOME and file secret backend;
GitHub CLI alone uses the existing verified `cnjack` keyring identity. No login
credential was copied or switched. A real helper timeout led to process-group
cancellation and bounded waits; a regression test proves children cannot survive.

Fresh jcode CI generation also exposed an existing stale catalog test: the live
Chinese Coding Plan catalog no longer lists GLM-5.2. Tests now exercise GLM-5.2 on
provider catalogs that still advertise it; no model availability was fabricated.

### Release candidate validation

Console 596 tests, typecheck, token lint and production build passed; full
Orchestrator Go with real PostgreSQL passed. Shared device UI 150 tests passed,
then the added encrypted draft command test and affected header/settings tests
passed (39 focused tests). The sibling full Go, build, vet and lint passed after
fresh catalog generation. Runner persistent reuse/protocol regression passed.
OAuth cancellation now explicitly covers completion racing cancellation and
idempotent retries preserving an existing login. Public acceptance is pending.

### Final release transport

The final Console CI at `e98a795` passed 598 tests, typecheck, token lint and
production build (run 35694806413); shared device UI passed 151 tests. The later
`6721a81` commit changes only release transport and its operational documentation.
`actionlint` passed. Aliyun upload stalled twice after GHCR publication. A separate
Docker-engine mirror task verified existing-layer access using the same CI
identity but also stalled on new layers. Local Docker had no push permission; no
credentials were replaced or extracted. Those diagnostic/superseded runs were
cancelled without changing production.

The explicit `publish_aliyun=false` dispatch (run 35696790296) publishes all core
and five Cube images to GHCR. Default dual-registry publication remains enabled,
and the release tag records mirror availability. Company Cube-host HTTPS access
to GHCR was verified. Deployment must use the new release's GHCR digests and
register all five templates with `--registry ghcr.io/cnjack`.

### Deployment checkpoint (2026-09-22)

Release `v0.0.159` completed at source `6721a81` in run 35696790296.
The company Orchestrator and migration init container use immutable digest
`sha256:04961afc2948b172ada698d7184668d76bffdeb7842f14e4fb66ca5c73c9b195`.
The rollout is ready; migrations 77–79 and the authenticated public system
version were verified. Console was held on `v0.0.158` until the five new Cube
templates became ready, then switched to `v0.0.159` at digest
`sha256:2d9026bb8f8b2ef86158eb159eb0e9efe0e3020da99f1a808c449e31bc29c8d6`.
Both public Deployments are ready and their pod image IDs match those digests.

The sibling device changes were merged in jcode PR 213 and released as
`v0.13.6` at `41cf89ab7306d9948d2d7a2358302dfd377eeda6`. The official macOS
arm64 CLI checksum and embedded version were verified without replacing the
user's installed binary.

The first default-template build failed on a registry HTTP/2 stream error.
Its failed receipt is retained. The replacement native job
`5a232b96-7cdb-4a41-b2f1-2dd68c512c53` also failed on an HTTP/2 stream error
after 47,869,765 bytes. Restarting the local observer with `--wait-timeout 21600`
had reused that same receipt and job; its later failure was native and terminal.
Observation expiry never cancels the native build. Read transport failures have
bounded retries; template creation is not retried implicitly. Four operational
unit tests pass. The user chose to keep waiting on the current download and
explicitly declined adding a temporary CI runner; none was registered.

Authenticated public acceptance is ongoing. Local encrypted device/draft
journeys do not substitute for public acceptance, and
personal ChatGPT authorization/inference still requires a real completed flow.

### Verified public-image transport recovery

After the second native download failed, the five public release images were
downloaded with HTTP/1.1 through the existing Mac network path, without a CI
runner or private registry credentials. All 46 distinct blobs passed size and
SHA-256 verification; all image configs identify Linux amd64. Manifest bytes
were retained unchanged. The default digest matches the failed native job's
published source digest exactly. The bundle was transferred over the existing
SSH connection, and all 46 blobs passed a second full hash check on the Cube
host. A temporary read-only source served only these public images to the host
and cluster network. All five native artifacts reached READY and their source
digests matched the published manifests. The complete mapping was applied and
read back before restarting Orchestrator; the subsequent Console rollout passed.

| Profile | Ready template |
| --- | --- |
| default | `tpl-68235888234c42e48d5a15c2` |
| go-node | `tpl-980bff750e874bf88ac4ea2c` |
| python | `tpl-b5a1fa11e2c94429b0946090` |
| rust | `tpl-1bc62cc897264fa69473743f` |
| polyglot | `tpl-f336c3a0ef844c1fa5d96fc7` |

### Authenticated public runtime acceptance

The existing Jack account created QA run `5a0b1f9fa9f7c9947d136a31fb49776d`
in `cnjack/jcode-cloud-workflow-e2e-20260801` through the new public Console.
The actual GLM-5.2 stream and tool events reported `/workspace`, UID `10001`,
`x86_64`, and a clean Git working tree. The browser individually approved the
`id -u` and `uname -m` requests; session-wide auto-approval was not enabled.
A follow-up executed `pwd` in the same conversation. Finish session reached
`succeeded`; PostgreSQL confirmed `checkpoint_saved=true` and cleanup completed.

Continuing the completed conversation created run
`6356b58c27161e7c312c8a2936cb45bf` through the real resume endpoint (201).
Its distinct sandbox `87349feeadb24ccebc4b437be2012a5e` uses the new Python
template, confirmed by the Cube API. It again returned `/workspace` and a clean
Git working tree. The approved metadata-only command reported
`root:jcode 750 /var/lib/jcloud-restore`, confirming the restore directory created
by bootstrap. Stop reached `canceled`; PostgreSQL again confirmed
`checkpoint_saved=true` and completed cleanup. The browser reported no warning
or error logs during these journeys. They did not commit, push, or create pull
requests. Remaining public page/mobile and personal authorization journeys are
still required; the overall acceptance is not complete.

### Public surface acceptance and follow-up corrections

The public account selected `cnjack/jcode` through the repository picker. Its
Board stays at the repository route and renders the real connected JType Board.
The existing binding reports `execution_account_not_owner`; acceptance did not
change its execution identity, permissions, columns, or model. The QA repository
shows its unconnected Board configuration. Existing automation values load in
the editor, and Cancel returns to the same repository. No user automation was
changed. Search, status filters, empty states, review details, account and
repository settings, usage, and cluster surfaces were inspected against live data.

The previously paired public browser decrypted an existing online device session.
The device runs v0.13.5 and correctly requires an upgrade for the new workspace
inspector. Its older session was read only. Remote device/session/preferences and
cluster connection pages were inspected at 390px without horizontal overflow.

Public acceptance found three recoverability/context gaps: repository automation
routes showed only Account in the global header, blocked Card output showed a raw
blocker code, and device setup lacked an upgrade link. Commit `d3d53cb` fixes those
with repository identity, localized reason and exact Board settings navigation,
and an official jcode release link in all five locales. Tests were first run red
(6 failures), then green: 602 Console tests, typecheck, token lint and build passed.

A personal QA connection named `Cloud acceptance ChatGPT` was created but received
no credential. A bounded diagnostic using the same fixed public client ID and
HTTP request from the production Orchestrator confirmed upstream JSON error
`unsupported_country_region_territory`, `request_forbidden`, HTTP 403. No auth
code or token was printed, and the diagnostic binary was removed. The production
exit region is unsupported by the provider; reauthorization alone cannot repair
this. Full personal ChatGPT authorization and inference remain externally blocked.

### Responsive acceptance correction

A 390px public screenshot exposed a clipped Start task button despite
`document.scrollWidth == innerWidth`: the inner workspace grid had an implicit
447px column, while its 390px parent hid the overflow. Commit `ea57545` constrains
that column with `minmax(0, 1fr)` and hides redundant account display-name text
on small screens while retaining the avatar, menu and accessible label. Layout
checks on the explicitly labelled local demo at 320px, 390px and 1440px verified
Start task and Language button bounds and readable repository context; these
layout-only checks do not substitute for public runtime acceptance. All 602
Console tests, typecheck, token lint and production build passed again. The final
Console release will include both acceptance fixes.

The temporary public-image mirror process on the Cube host was stopped after all
five artifacts were ready and a public Python task had used the new template.
The temporary bundle/source directory were removed; a fresh connection check
confirmed port 18769 was no longer listening. Native templates and production
Runner pins remain at the verified v0.0.159 artifacts.

A second public mobile interaction check found that the composer toolbar could
paint above the navigation drawer and intercept the Remote devices link. A DOM
hit test confirmed the intercepted element, rather than a routing/API failure.
Commit `b4b8935` gives the Home, utility and Run surfaces independent stacking
contexts below the rail, and applies the same bounded grid column to Run detail.
The local mobile regression verified that the hit target is now `/devices`, the
drawer is visually unobstructed, and clicking navigates and closes the drawer.
All 602 Console tests, typecheck, token lint and production build passed.

Public v0.0.161 readback confirmed the automation editor header links to the
same repository Automations tab. Its disabled Card option now shows localized
`Automation disabled` copy, and Open Agent Board settings navigates to that
repository's Board. Device setup exposes the official latest-release URL. These
checks also passed with the account UI rendered in Simplified Chinese.

### Public mobile navigation and cache verification

After v0.0.162 rollout, the public browser verified 390px Home → Devices,
QA Run → Devices and encrypted Remote conversation → Account settings. DOM hit
tests identified the intended links, each click reached the expected route, and
content stayed within the viewport. No browser warning/error logs were recorded.
The Run and Remote routes used existing records; no user conversation was sent to.

A direct navigation to `/` initially reused an old cached entry document even
though both the live Pod and a fresh public HTTP request returned the new bundle.
Browser Reload revalidated it and loaded the exact v0.0.162 assets. Commit
`60dc9f2` adds `Cache-Control: no-cache` to the SPA handler. Real nginx:1.27-alpine
checks verified `/`, `/devices`, and `/index.html` return HTML with no-cache.
The same check exposed that the share handler's internal redirect dropped its
existing privacy/CSP headers. Serving index.html inside the share location keeps
`no-store`, `no-referrer`, and `frame-ancestors 'none'`; the real `/s/` HTTP test
passed. The temporary nginx container was removed. Console 603 tests, typecheck,
token lint and build passed, plus the final affected configuration tests.

### Final deployment record (2026-09-22)

Console `v0.0.163`, source `60dc9f2df9c6a78a3bddc049920c5a35de7742a7`,
completed image run `35708840658` and Console CI `35708835824` successfully.
The live Console image and ready Pod `console-8558c96dcd-bfdc8` match immutable
digest `sha256:9594e0132f0dfd97313619c89b2240f0af58c8aaed2ba304b36548a6f9d45925`.
Orchestrator, migrations and all five runtime template/image pins remain on
the verified `v0.0.159`; the subsequent changes are Console-only.

The public `/` and `/devices` endpoints returned HTTP 200 with no-cache. The
public `/s/header-acceptance` route returned the static viewer entry with no-store,
no-referrer and the intended frame-ancestors policy; no artifact was created.
Live nginx configuration validation passed. The authenticated browser reloaded
and rendered bundle `index-BgUGYCYN.js`, matching the fresh public HTML and live
image, and the final desktop homepage screenshot was inspected. Browsers holding
a document cached before this fix may need a refresh once to obtain the new
response policy; the release URL can include a version query to avoid old entries.

All requested product surfaces are implemented and deployed. Full personal
ChatGPT authorization/inference remains unverified because the upstream rejects
the production exit region. The user has been asked whether to accept the current
model journeys and retain ChatGPT as a follow-up; no reduced scope is assumed
without their answer. Existing remote clients must upgrade to released jcode
v0.13.6 to use the new workspace inspector/draft delivery capabilities; public
old-client gating and existing encrypted sessions were verified, while new
capabilities have real encrypted local integration and draft-PR evidence above.
