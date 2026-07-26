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
| Runtime | Inject matching Skill/CLI/MCP at Run start, never in generic Runner | needs live verification | snapshot, fail-closed injector, read-only mount, and image-content tests exist; real `gh`/`glab`/`tea` and Git credential-helper invocations remain |
| Legacy Console | Remove obsolete Integration/Kanban/PAT settings paths | automated verification passed | obsolete components, client/types/mocks, PAT flow, and Project Settings branches removed; dead-code search and focused Console tests pass |
| Localization | Translate all Plugin detail UI and trigger labels | automated verification passed | Plugin detail and Automation labels use all five locale catalogs; focused Console tests/typecheck pass |

## Task-composition enhancements

| Option | Product rule | Status | Completion evidence |
| --- | --- | --- | --- |
| Branch | Manual tasks choose a repository branch; triggered tasks resolve event/filter/default branch deterministically | automated verification passed | live branch listing/validation, persistence, event resolution, Job env, and Console tests; see the unpinned-ref P2 below |
| Model effort | Choose `auto` or a model-supported reasoning effort and pass the resolved value at startup | automated verification passed | model capability validation, persistence/retry/resume, generated config, and Job assertions |
| Goal mode | Initialize a concrete goal through jcode's native goal startup contract | automated verification passed | persistence/retry/resume and native `/goal` startup assertions |
| Attachments | Upload Project-bound files safely and expose them read-only to only the target Run | automated verification passed | auth, filename/content-type/length limits, stage state machine, quota, lifecycle, retry/resume, read-only manifest, tmpfs and memory-accounting tests, including real-PG concurrent stage/quota/claim and last-reference GC coverage |

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
- [ ] Kustomize targets render with exact release images.
- [x] Generic Runner image contains no Provider CLI or Skill.
- [x] Domain/data consistency review has no unresolved release blocker.
- [x] Attacker-perspective review has no unresolved release blocker.
- [ ] Branch is pushed and image workflow succeeds for GitHub Packages and
      Aliyun Registry.
- [ ] PostgreSQL backup is taken before deployment.
- [ ] Production migrations, readiness, and rollout are healthy.
- [ ] GitHub push and `@jcode` journeys pass with a configured model.
- [ ] GitLab/Gitea journeys pass, or remain explicitly blocked with the exact
      external configuration still required.
