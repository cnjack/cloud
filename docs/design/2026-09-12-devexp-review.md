# Cloud runtime migration and developer experience review

Verified online on 2026-09-12: Orchestrator v0.0.156 and Console v0.0.157, using the
verified v0.0.155 Cube Runner templates. Success, provider failure, checkpoint
readback and sandbox cleanup passed. The linked Board displays its 20 cards.

## Review scope and acceptance cases

Follow a real account from repository selection through a CubeSandbox session,
approval, terminal result, retry, and the linked JType Board. Preserve the D32
account workspace, repository picker and shared composer. Recovery must explain
the failing dependency and never fabricate success.

Acceptance cases used before the corresponding implementation:

- Switching from a completed Run to a newly queued Run must start the new stream;
  the new cache and transcript must never receive the previous Run's state.
- Mutation responses must retain GET-only provenance, usage and SCM projections
  until an authoritative detail refresh arrives.
- Approval cards must show the agent's actual sanitized arguments, even when the
  permission event arrives before its tool event. Older records explicitly say
  arguments were not recorded.
- A successful ACP RPC carrying `refusal`, `cancelled`, or an unknown stop reason
  must not report a completed model turn. Valid token/turn limits retain the
  existing resumable turn behavior.
- JType certificate verification errors must return an actionable typed 503;
  they must not be described as a deleted Board or rejected account.
- The real Board must load its columns/cards and leave room for them on screen;
  automation settings remain accessible, with the policy state always visible.

## Findings and implementation review

| Finding | Cause | Change |
| --- | --- | --- |
| Retry briefly shows the previous transcript/status | Hook state survives the route change before effects reset it | Bind reducer state and dispatch to Run identity |
| Source/usage fields disappear after actions | Raw mutation Run replaces enriched GET projection | Merge projection fields and invalidate detail query |
| Status row displays a translation key | Wrong i18n namespace | Use `run.statusName` |
| Approval lacks the actual command | Runner drops ACP RawInput; UI renders permission options | Carry sanitized arguments through event and shared approval model |
| A refused model request can end as successful | Multi-turn driver ignores ACP StopReason | Fail before turn hook and turn-complete notification |
| Model subscription failure says key rejected | Alibaba returns 403 AccessDenied.Unpurchased | Classify as unavailable model access in jcode and give activation guidance |
| Board fails to load | Public JType HTTPS certificate expired September 2 | Native SafeLine ACME certificate for jtype.nightc.com, bound only to site 212 |
| Board starts below the first screen | Landing hero plus expanded automation editor | Wide Board layout, omit welcome copy on Board, collapse configuration |
| Connection diagnostic points at localhost | Static placeholder | Display actual same-origin API address |

The certificate change uses SafeLine's existing ACME lifecycle (type 3), valid
through 2026-12-11. The previous shared wildcard certificate remains unchanged
for other sites. Site 212's prior configuration was backed up on its gateway.
Public HTTPS returned 200 with normal certificate verification. Board readback
showed 20 real cards across Backlog, To do, Doing and Done.

The existing Board automation has a separate execution-account ownership
blocker. It stays visible and blocked; this review does not silently enable
old queued Cards or select a different execution account/model.

## Verification

- Orchestrator full Go suite passed, including typed TLS failure regression.
- Runner ACP subprocess/control-plane tests passed, including approval arguments
  and unsuccessful stop reasons never posting turn-complete.
- Console: 72 files / 559 tests passed; typecheck passed.
- Shared device UI: 20 files / 147 tests passed.
- jcode model package tests passed, including Unpurchased vs generic invalid-key
  403 and actionable copy. Full jcode pre-push validation follows its hook.
- Local Node 26 exposed jsdom localStorage incompatibility; Node 20 exposed a
  WebCrypto cross-realm incompatibility. The complete suite passed with the
  bundled Node 24.19.0. CI uses Node 22.

Runtime PoC, design decisions, workspace migration and cleanup evidence are in
[cubesandbox-runtime.md](cubesandbox-runtime.md). Final release and public smoke
results are recorded after deployment below.

## Release and public verification

- UI/Runner feedback source: `90be07cfb96455bcbea69dd02e4a6ee4594d8c01`.
- Final control-plane source: `a82a5de952df87ea8e9dc3f00a77c02b52b19025`.
- v0.0.154 images and all five templates passed. The final model diagnostic
  correction is bundled from jcode `1166a8e5217ddfff11b30cd59d1ff602b93ecbe5`
  in v0.0.155; its full pre-push checks and GitHub CI passed.
- [v0.0.155 image workflow](https://github.com/cnjack/cloud/actions/runs/34686299261)
  and [jcode CI](https://github.com/cnjack/jcode/actions/runs/34686277782) passed.
- Actual provider failures put `AccessDenied.Unpurchased` in `APIError.Code`,
  separately from the human message. The final regression covers that structure
  through a wrapped error, alongside text-only and generic invalid-key cases.
- The jcode pre-push hook now clears checkout-specific Git environment variables.
  Otherwise tests initializing temporary Git repositories inherit a linked
  worktree's `GIT_DIR`. Full checks passed after isolation; the original local
  UI changes remained intact.

| Public Run | Version | Result and evidence |
| --- | --- | --- |
| `b4e577642041fb93c6490cc1237126f4` | 154 | Retry immediately showed the new queued state without old transcript; model rejection ended Failed, checkpoint saved, VM removed at 09:33:24 UTC. |
| `9484fa96a1d04b782a8aae64411813aa` | 154 | Switched to the authorized Zhipu model. Actual approval command and expanded JSON parameters appeared before execution. README and 7 environment probes succeeded under UID 10001; git remained clean. Finish preserved provenance/usage, saved the checkpoint and removed the VM at 09:37:39 UTC. |

All approval decisions in these checks were one-time approvals of the displayed
read-only commands. The old five prewarm Pods and their DaemonSet remain removed.
PostgreSQL, unrelated workloads and source workspace PVC backups are retained.

A second live check exposed the remaining transport cause: Alibaba's error JSON
was gzip encoded. Forwarding the caller's `Accept-Encoding` disabled Go's automatic
upstream decompression, so Cloud's error normalizer replaced valid compressed JSON
with `upstream_http_403`. The new HTTP regression reproduced that exact loss before
the fix. The proxy now lets its own transport negotiate/decode compression before
error normalization and usage inspection. The full Orchestrator suite passes.

The deployed v0.0.155 guest binary was independently checked: its embedded VCS
revision is `1166a8e5217ddfff11b30cd59d1ff602b93ecbe5`, confirming that the remaining
fault was the proxy, rather than a stale template. The disposable inspection VM
was deleted. The subsequent control-plane release reuses these verified templates.


## Final acceptance

[The v0.0.156 image workflow](https://github.com/cnjack/cloud/actions/runs/34687019710)
passed. Orchestrator and its migration init container are pinned to v0.0.156;
Console subsequently moved to v0.0.157 for the Card spacing fix. Runner images
and all five template IDs remain pinned to verified v0.0.155.
Both Deployments are Ready, schema version is 76, and the public Console served
v0.0.156 during the following checks:

- [Provider failure Run](https://cloud.j-code.net/runs/2f3ea6dd069c239d139a9c3c1b7ceddc):
  now displays “Model access is not enabled” with activation/switch-model guidance;
  ends Failed, saves its checkpoint and removes its sandbox (10:02:10 UTC).
- [Successful Run](https://cloud.j-code.net/runs/395ae322558d1469a1c5c33691dc4a9d):
  executes `pwd` against the real Cube guest and returns `/workspace`. Finish
  retains provenance and captures 2/2 usage requests; the Run ends Succeeded,
  saves its checkpoint and removes its sandbox (10:05:55 UTC).
- Final provider listing and registry readback: zero Cloud-owned or inspection
  sandboxes remain. The legacy prewarm DaemonSet and Pods are absent.
- The temporary PostgreSQL test container, inspection VM, development worktrees
  and migration stash were removed. Existing unrelated jcode/jtype edits remain.

Environment-specific template IDs and the final deployment snapshot are retained
locally in `deploy/cubesandbox/final-verification.generated.json`. UI screenshots
are local artifacts, rather than public repository assets.

## Card spacing follow-up

The user's intended layout has uniform space between cards. Browser measurement
showed the actual adjacent card gap was 0px: Cloud's unlayered universal margin
reset overrode JType's layered `space-y-2` utility. The reset now belongs to the
base layer, allowing the Board's native 8px spacing to apply. The compact-screen
column editor rule was also corrected to retain its single-column layout.

Acceptance checks: differently sized adjacent cards have 8px gaps; the last card
has no extra trailing margin; ordinary host paragraphs still have 0px margins.
The browser probe using the real application/Board styles passed those checks.
All 559 Console tests, typecheck and production build passed with the same shared
UI revision used by CI. Console v0.0.157 was then deployed after the
[matching image workflow](https://github.com/cnjack/cloud/actions/runs/34690516613)
passed. Public DOM measurement found all 20 cards; all 17 adjacent-card pairs
(11 in Backlog and 6 in Done) measured exactly 8px. The real Board screenshot
confirms the restored separation. Source commit: `6e96cd8343e16462c3eb60cad17f60a05adfaac5`.
