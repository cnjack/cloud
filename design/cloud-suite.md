# Unified Cloud design suite

Interactive proposal extending `work-home-refresh.html`. Not production code.

## Verification plan

- Every link within the suite remains in the same shell and resolves locally.
- Repository tabs retain repository identity; recent conversations open actual
  detail pages. Browser back and reload retain meaningful context.
- Board card detail, review findings, automation editing and repository settings
  have usable interactions, not links to legacy Project prototypes.
- Personal profile, Git connections, models, preferences and account usage are
  distinct from repository settings and usage.
- Device list → device workspace → remote conversation and connect-device flows
  are navigable. Offline blocks sending; an unpaired browser cannot show content.
- Cloud conversation shows message/tool history, approval, failed and completed
  states, change inspection and follow-up composition.
- Every page is checked at desktop and mobile widths for errors, broken assets,
  overflow, visible focus and recoverable empty/unavailable states.
- Local preview actions explicitly identify simulated changes. No real task,
  approval, OAuth, credential, pairing, automation or deployment is performed.

## Source boundaries and proposals

Current sources: `console/src/App.tsx`, `work-home/WorkHomePage.tsx`,
`pages/AccountSettingsPage.tsx`, `pages/DeviceGuidePage.tsx`, and
`docs/17-jcode-device-relay.md` (login, pairing, encryption).

Current runtime already redirects `/projects/*` to Work Home. The product
hierarchy here is Account → Repository → Task. Internal legacy Project/Service
storage does not become a user-facing navigation layer.

The new dedicated device list and device workspace are design proposals:
current `/devices` routes to the guide and legacy device detail redirects to
Work Home. Implementation must add/update Console routes while retaining
account-scoped device access and browser-side decryption/pairing gates.
No new backend capability is claimed by the static fixture data. Board cards
belong to the connected jtype board; moving a local preview card never proves
that jtype changed or an agent ran. Usage is a labelled sample, not telemetry.

Each major surface has its own HTML document. Shared assets are
`work-home-refresh.css`, `cloud-suite.css`, `cloud-suite.js` and canonical icons.
The home retains its focused interactions in `work-home-refresh.js`.
Session storage uses only the `jcode-design:` namespace for preview context and
drafts, never actual configuration, credentials or encryption keys.

## Page map

| File | Surface |
| --- | --- |
| `work-home-refresh.html` | Repository task home |
| `cloud-board.html` | Connected board, card detail and column selection |
| `cloud-reviews.html` | Pull request review history and new request |
| `cloud-review-detail.html` | Commit-scoped review findings |
| `cloud-automations.html` | Automation list and execution history |
| `cloud-automation-edit.html` | Trigger and task editor |
| `cloud-usage.html` | Repository usage; `scope=account` for account usage |
| `cloud-repository-settings.html` | Repository execution and delivery defaults |
| `cloud-conversation.html` | Cloud conversation, approvals, diff and follow-up |
| `cloud-devices.html` | Account device list |
| `cloud-device.html` | Device workplace and recent sessions |
| `cloud-device-connect.html` | Device login, authorization and pairing |
| `cloud-remote-conversation.html` | Remote conversation and pairing/offline gates |
| `cloud-settings.html` | Personal profile |
| `cloud-settings-connections.html` | Git accounts and reauthorization |
| `cloud-settings-models.html` | Model providers and default model |
| `cloud-settings-preferences.html` | Personal defaults and UI preferences |
| `cloud-admin.html` | Cluster overview and shared execution policy |
| `cloud-map.html` | Navigation to the complete suite |

## Verification — 2026-09-22

Local Chrome/Playwright checked all 19 documents at 1440×1000 and 390×844,
plus the 18 new documents at 768×1024. No document-level horizontal overflow,
page errors, failed HTTP responses, missing linked HTML files, or links back to
legacy prototypes were found. Representative desktop/mobile screenshots were
visually inspected for board, conversation, models, devices, pairing and remote
conversation; account connections and the navigation directory were also checked.

Interaction checks covered repo/tab continuity, draft and composer choices,
attachment filename previews, card moves, conversation navigation, approvals,
follow-ups, sample diff, stopped-state continuity, account form restoration,
model reauthorization, usage scope, automation triggers, offline send blocking,
pairing expiration/retry, hidden unpaired conversation titles/content, and remote
session identity. JavaScript syntax and Git whitespace checks passed.

Prototype state is deliberately local. No production source, API, cluster,
credentials, remote device, Git provider or real task was changed.
