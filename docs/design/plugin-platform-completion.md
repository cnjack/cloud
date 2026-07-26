# Plugin platform completion tracker

This tracker records the implementation and verification work approved on
2026-07-26. The behavioral contract remains
[`plugin-platform.md`](plugin-platform.md); this file records execution state
without weakening that contract.

Status vocabulary:

- `in progress`: implementation or automated verification is being completed;
- `automated verification passed`: the implementation and self-contained test
  evidence exist; this does not replace a required real-Provider, rollout, or
  browser check;
- `needs live verification`: code and self-contained tests exist, but a real
  configured Provider journey has not passed;
- `blocked by external configuration`: Cloud cannot create the external
  Provider account, OAuth application, repository, or permissions itself;
- `done`: implementation, automated tests, review, and required live check
  passed.

## Required work

| Area | Deliverable | Status | Completion evidence |
| --- | --- | --- | --- |
| Retention | Delete expired `webhook_receipts` in bounded periodic batches and reclaim unreferenced historical Plugin secret versions | automated verification passed | PG/Mem/reconciler coverage for expiry, bounded cleanup, authenticated-payload dedupe, active/pending-Kanban-writeback retention, and terminal snapshot audit IDs; production observation remains a release check |
| Provider health | Authenticated GitHub App probe plus honest instance/grant probes for GitLab, Gitea, and JType | needs live verification | provider probe fixtures cover App identity, public and grant-authenticated version endpoints, partial GitLab responses, JType health, and sanitized failures; each configured production Provider still needs a real probe |
| Capabilities | Fail closed and degrade unsupported actions from observed version/features | needs live verification | version parser, API matrix, Automation rejection, and Console disabled-action tests exist; production Provider versions remain unchecked |
| GitHub SCM | Real push creates an Automation-origin Run | needs live verification | fixture-to-Run tests plus Mem/real-PG concurrent coalescing prove one queued latest Run; production Run, receipt, origin, and result remain |
| GitHub mention | New `@jcode` comment passes the complete comment to one Run | needs live verification | actor, prompt, dedupe, no Cloud writeback |
| GitHub events | Preserve the full event catalog; promote common actions and disclose the rest | automated verification passed | executable GitHub event-matrix normalization plus Console common/“More events” selection tests |
| GitLab | OAuth → Installation → Service → hook → Run → CLI → uninstall | blocked by external configuration | disposable instance/project journey |
| Gitea | OAuth → Installation → Service → hook → Run → CLI → uninstall | blocked by external configuration | disposable instance/repository journey |
| Runtime | Inject matching Skill/CLI/MCP at Run start, never in generic Runner | needs live verification | immutable-snapshot Provider filtering, fail-closed injector, three managed Skill masks, Provider-scoped env/CLI/Skill tests, runner-read-only credential tmpfs, `gh` 2.94 current-schema config, and image-content tests exist; real `gh`/`glab`/`tea` and Git credential-helper invocations remain |
| Legacy Console | Remove obsolete Integration/Kanban/PAT settings paths | automated verification passed | obsolete components, client/types/mocks, PAT flow, and Project Settings branches removed; dead-code search and focused Console tests pass |
| Localization | Translate all Plugin detail UI and trigger labels | automated verification passed | Plugin detail and Automation labels use all five locale catalogs; focused Console tests/typecheck pass |

## Task-composition enhancements

| Option | Product rule | Status | Completion evidence |
| --- | --- | --- | --- |
| Branch | Manual tasks choose a repository branch; triggered tasks resolve event/filter/default branch deterministically | automated verification passed | live branch listing/validation, persistence, event resolution, Job env, and Console tests; see the unpinned-ref P2 below |
| Model effort | Choose `auto` or a model-supported reasoning effort and pass the resolved value at startup | automated verification passed | model capability validation, persistence/retry/resume, generated config, and Job assertions |
| Goal mode | Initialize a concrete goal through jcode's native goal startup contract | automated verification passed | persistence/retry/resume and native `/goal` startup assertions |
| Attachments | Upload Project-bound files safely and expose them read-only to only the target Run | automated verification passed | auth, filename/content-type/length limits, stage state machine, quota, lifecycle, retry/resume, read-only manifest, tmpfs and memory-accounting tests, including real-PG concurrent stage/quota/claim and last-reference GC coverage |
| Composer parity | Keep the Cloud new-task composer visually and behaviorally aligned with jcode while preserving Cloud-specific Service, base-branch, staged-file, and one-shot Run semantics | production verification passed | jcode-style composer with a unified `+` menu, visible Goal state, permission/branch/model/effort/send footer, Enter-to-send, Shift+Enter newline, IME protection, hidden-input focus discipline, attachment-limit keyboard handling, five locales, narrow-screen layout, 545-test Console regression, and production browser task creation |

## Runtime contract

The production runtime now has the following explicit boundaries:

- The Provider set comes from the immutable Run Plugin snapshots created during
  dispatch. Only an enabled, healthy Installation whose current Provider config
  is enabled and revision-matched can enter that set.
- GitHub, GitLab, and Gitea receive their matching environment variables, CLI,
  and managed Skill only when that Provider is present in the snapshot. A Run
  cannot advertise a managed Provider Skill when the corresponding CLI was not
  injected. JType remains MCP-only and never receives a managed Skill or CLI.
- Every Plugin Run masks the three managed global Skill paths (`github`,
  `gitlab`, and `gitea`) with directories from the Run-scoped memory
  `EmptyDir`. A selected Provider's directory contains its `SKILL.md`; an
  unselected Provider's directory is empty. This prevents a stale managed Skill
  in persistent jcode HOME from leaking into a later Run.
- Every Run emits the forward-compatible `JCODE_RESERVED_SKILLS` policy and
  Plugin Runs point `JCODE_MANAGED_SKILLS_DIR` at the read-only Run-scoped Skill
  root. These variables are inert in the current jcode release; enforcing
  reserved names and slash triggers remains an optional future behavior under
  evaluation in [cnjack/jcode#175](https://github.com/cnjack/jcode/pull/175).
- The credential initializer and refresh sidecar are the only writers to the
  credential memory `EmptyDir`. The runner mounts all Provider CLI, Git helper,
  and JType MCP credential configuration read-only. Refresh uses atomic file
  replacement, and access tokens are never written to the workspace PVC or
  persistent jcode HOME.
- The runtime image pins GitHub CLI `2.94.0`. Its generated `hosts.yml` uses the
  current per-user schema, and `gh/config.yml` contains the `version: "1"`
  marker. This prevents `gh` from attempting a legacy-schema migration against
  the runner's deliberately read-only configuration.

## Accepted Skill-shadowing risk

On 2026-07-27 the product owner explicitly accepted releasing without merging
[cnjack/jcode#175](https://github.com/cnjack/jcode/pull/175) while the overwrite
semantics receive further evaluation.

A repository-owned project Skill can therefore use a Provider name or slash
trigger such as `github` or `/github` and override the Cloud-managed Skill in
jcode's discovery layer. This is accepted because the connected Git repository
is treated as user-controlled task input. It does not create a Plugin
Installation, add a Provider to the immutable Run snapshot, inject a missing
CLI, or grant a token that the Run did not already receive.

The Cloud runtime still masks stale global Provider Skill directories for every
Plugin Run, injects CLI/Skill/MCP assets strictly from the immutable snapshot,
and scopes CLI environment variables to the selected Providers. PR #175 remains
Draft as a potentially valuable hardening option, not a release dependency.

## Known P2 follow-ups

1. **Branch selection is not commit-pinned.** Run creation verifies that the
   selected branch exists and persists its name, but clone resolves that branch
   again when the Job starts. A force-push or normal update between creation and
   clone can therefore change the checked-out commit. Pinning the observed SHA
   is a separate consistency improvement.
2. **Automation mutation and Provider webhook reconciliation are not one
   distributed transaction.** The Automation is committed first. If external
   hook creation or deletion fails, the API returns
   `502 webhook_reconcile_failed`, the committed resource remains inspectable,
   and both the Automation and `webhook_bindings.last_error` expose the stable
   retry guidance. Saving/enabling the Automation again retries reconciliation.
   A background external-state reconciler remains a follow-up.

## Release gates

- [x] Orchestrator full test suite passes.
- [x] PostgreSQL-gated store suite passes.
- [x] Console tests, typecheck, localization, and production build pass.
- [x] Kustomize targets render with exact release images.
- [x] Generic Runner image contains no Provider CLI or Skill.
- [x] Domain/data consistency review has no unresolved release blocker.
- [x] Attacker-perspective review has no unresolved release blocker.
- [x] Branch is pushed and image workflow succeeds for GitHub Packages and
      Aliyun Registry.
- [x] PostgreSQL backup is taken before deployment.
- [x] Production migrations, readiness, and rollout are healthy.
- [x] Project-local Provider Skill overwrite risk is explicitly accepted for
      this release; `cnjack/jcode#175` remains Draft for further evaluation.
- [ ] GitHub push and `@jcode` journeys pass with a configured model.
- [ ] GitLab/Gitea journeys pass, or remain explicitly blocked with the exact
      external configuration still required.
