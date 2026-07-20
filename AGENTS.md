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
     device-relay demo rig.
   - Manifest changes: render each affected Kustomize target before delivery.
5. **Commit cleanly.** Use Conventional Commits and keep one coherent feature
   in each commit.

## Instruction maintenance

- Keep this file in English and focused on durable, cross-agent rules.
- Put temporary task context in the task or pull request, not here.
- `CLAUDE.md` is intentionally only a pointer to this file. If a rule needs to
  change, update `AGENTS.md` rather than expanding `CLAUDE.md`.
