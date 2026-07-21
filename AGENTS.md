# jcode Cloud Agent Instructions

`AGENTS.md` is the canonical source of durable project instructions for every
coding agent. Read it before making changes.

If a project instruction needs to be added or changed, update this file in the
same change. Do not duplicate long-lived rules in tool-specific instruction
files.

## Product non-negotiables

### Treat approved designs as product requirements

When the user provides or approves a design, treat the behavior and information
architecture in that design as the desired product contract, not as optional
visual inspiration.

- Do not silently omit, downgrade, or replace designed behavior just because the
  current API or domain model cannot support it.
- If implementation requires a new API, persistence model, provider capability,
  or other architectural work, report that gap explicitly before narrowing the
  scope. Explain what is missing and what must change.
- Extend the underlying contracts needed to implement the design unless the user
  explicitly agrees to a staged or reduced scope.
- The fail-visibly rule applies to product behavior as well as runtime failures:
  an unsupported design requirement must be surfaced, never quietly hidden.

### Fail visibly

An unavailable dependency must be a first-class, visible state. Never silently
substitute a mock, fake implementation, or fallback that looks successful.

- APIs return typed, actionable errors—for example, `409` with
  `model_not_configured`—rather than fabricated success data.
- The UI disables an action when its required dependency is unavailable and
  explains the next step, such as configuring a model or contacting an
  administrator.
- Webhook and automation paths record or report why work could not run. They
  must never claim a result that was not produced.
- Product manifests and default configuration must not point to `mockllm`,
  `FakeProvider`, or similar test doubles. Mocks belong only in tests and
  explicitly labelled end-to-end rigs.

### Credential discipline

- Keep real credentials only in gitignored `deploy/secrets/*.local.yaml`
  files. Never commit them.
- Do not change Gitea OAuth `redirect_uris` through an API `PATCH`: it rotates
  the client secret. Change it through the provider UI instead.

## Device relay (jcode ⇄ jcloud)

The device-relay feature lets local jcode instances log in (RFC 8628 device
code), be remote-controlled over an outbound-only relay, and renders in
console/mobile — all end-to-end encrypted. Durable rules for this area:

- **Design docs are the contract.** `docs/17-jcode-device-relay.md` (relay,
  E2EE, pairing) and `docs/18-device-mesh-dispatch.md` (dispatch, design-only).
  Change the doc first when deviating; record implementation reality back into
  the doc (field names, status codes).
- **Ciphertext discipline.** Everything under the device namespace
  (`device_sessions.meta`, `device_events.envelope`, command payloads,
  `devices.capabilities`) is an opaque envelope — never parse it server-side.
  Plaintext is limited to routing metadata: ids, seq, kind, timestamps, online
  state. New write paths must preserve this; e2e proves it with psql
  zero-plaintext greps.
- **Migrations are append-only and idempotent** (`IF NOT EXISTS`, guarded
  `DO $$ ... $$` blocks). New device tables need FK `ON DELETE CASCADE` chains
  up to `users` — `0030` missed `device_events` and orphaned rows (fixed in
  `0031`); always test cleanup-by-user-delete.
- **e2e journeys** (`e2e/j*.sh`, keyed `Jx-Sn`): `j7` login, `j8` relay,
  `j9` E2EE, `j10` QR pairing, `j11` compose facets. They run against the
  OrbStack stack (context `orbstack`, ns `jcloud`), seed a user session
  directly via psql (`HashToken` = sha256-hex), and drive a real `jcode web`
  against in-cluster mockllm. Reuse `lib.sh` helpers; register new journeys in
  `e2e.sh` (`ONLY=` + teardown cleanup).
- **Upgrade order: orchestrator before connector.** Old orchestrators reject
  upserts carrying new top-level fields (strict decode).
- **Deploy** to the company cluster (context `wangwenhui@local`, ns `jcode`):
  push the branch, `gh workflow run images.yml --ref <branch>` (builds amd64
  images, pushes ghcr `latest`, cuts a release tag only on main), then
  `kubectl rollout restart deploy/orchestrator deploy/console`. Verify
  `schema_migrations` and smoke the public path (no port-forward) afterwards.

## Engineering workflow

### Keep design prototypes page-scoped

- Put each product page or major routed state in its own HTML document under
  `design/`. Do not combine unrelated screens into one monolithic HTML file.
- Reuse shared styles, icons, and prototype-only interactions from
  `design/assets/` so separate page files still express one design system.
- For concepts shared with the sibling `jcode` product—especially models and
  providers—use `../jcode` as the interaction and visual reference. Reuse its
  canonical application and provider icons instead of inventing letter tiles.

For each feature or bug fix:

1. **Design the tests first.** List the cases before implementation. Mock
   dependencies only where needed, or run a real local dependency when that is
   practical.
2. **Implement in small, reviewable steps.** Follow nearby naming and comment
   conventions. Preserve unrelated changes in a dirty worktree.
3. **Review the diff independently.** Resolve confirmed defects before
   delivery.
4. **Run proportionate verification.**
   - Orchestrator changes: `cd orchestrator && go test ./...`
   - Console changes: `cd console && pnpm test && pnpm typecheck`
   - Shared device-UI package (console/packages/device-ui): also
     `pnpm --filter @jcloud/device-ui test`; its locale bundles are generated —
     re-run `node console/scripts/extract-device-locales.mjs` after editing the
     console's `device.*` copy.
   - Mobile app (cloud/mobile): `pnpm build` (web) and
     `cargo check` (src-tauri); `scripts/rig.sh up` brings up the local
     device-relay demo rig. `src-tauri/gen/` is gitignored and regenerated by
     `tauri android/ios init`; after a regen, re-apply the M11 native deltas:
     Android manifest `CAMERA` permission + `uses-feature camera
     required=false`, iOS Info.plist `CFBundleURLTypes` (scheme `jcode`) +
     `NSCameraUsageDescription`. The deep-link scheme itself is tracked in
     `tauri.conf.json > plugins.deep-link` and re-injected at build.
   - Manifest changes: render each affected Kustomize target before delivery.
5. **Commit cleanly.** Use Conventional Commits and keep one coherent feature
   in each commit.

## Instruction maintenance

- Keep this file in English and focused on durable, cross-agent rules.
- Put temporary task context in the task or pull request, not here.
- `CLAUDE.md` is intentionally only a pointer to this file. If a rule needs to
  change, update `AGENTS.md` rather than expanding `CLAUDE.md`.
