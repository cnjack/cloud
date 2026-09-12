# CubeSandbox runtime migration

Status: PoC, confinement, guest lifecycle and existing-workspace restore
verification passed, 2026-09-12. Release and production validation remain gates.

## Required behavior and test cases (before implementation)

The runtime must preserve the existing Run/Service contract: real jcode execution,
events and artifacts, non-root execution, timeout and cancel, Project Plugin CLI
and credential refresh, immutable model configuration, attachments, persistent
workspaces, session resume, archive/restore and deletion. Profiles must resolve
to administrator-owned immutable templates, never arbitrary task-supplied images.

1. Real Cube API create, command, file write/read, reconnect and kill, with final
   readback proving no PoC sandboxes remain.
2. A persistent volume survives destruction and replacement of a sandbox; git
   commit/status, executable permissions and symlinks work on it.
3. A dedicated runner template runs jcode as UID 10001, has no Kubernetes token,
   receives only selected Plugin assets, and exposes configuration read-only.
4. Duplicate create, control-plane restart, ambiguous create response, failed
   bootstrap, timeout, cancellation and failed deletion do not produce duplicate
   execution or falsely report successful cleanup.
5. All existing profiles, model settings, attachments, session turns and archive
   paths retain their behavior. Unavailable dependencies fail visibly.
6. Existing workspace backups and migration hashes match before traffic switches.
7. Go suite, PostgreSQL integration checks, rendered deployment manifests and a
   real production run through the public Cloud URL pass before old pods go away.

## Discovery

The company Cloud cluster is `wangwenhui@local`, namespace `jcode`. It currently
has five `jcloud-runner-prewarm` pods and two service workspace PVCs. PostgreSQL,
Console, Orchestrator and unrelated application pods are outside runtime cleanup.
Sibling `jcode` and `jtype` checkouts contain unrelated uncommitted changes.

CubeSandbox lives on `192.168.10.194`, in a separate Kubernetes cluster. Its
WebUI is `:30085`, CubeAPI is `:31000`, and CubeProxy is `:30080` with virtual Host
`49983-<sandbox-id>.cube.app` for envd. The deployed version is `v0.7.0-rc2`.
The control-plane endpoint currently accepts unauthenticated LAN requests; the
adapter must support API keys without changing shared Cube authentication as an
incidental part of this migration. Never log sandbox access tokens or run secrets.

## Proposed implementation

Keep the reconciler's `JobLauncher` seam. Add `JOB_LAUNCHER=cubesandbox` with an
explicit template allowlist for runtime profiles. Each Run owns one fresh VM
with a native ext4 working directory. Per-service object-storage checkpoints
replace PVC storage: restore before execution; stop all runner processes and
save checkout plus jcode memory/session state before destroying the VM. This
allows profile changes without retaining an obsolete VM/toolchain. Existing
archives use the same filesystem paths and remain restorable.

Cube resources carry a deployment owner and deterministic Cloud identity. Durable
dispatch and checkpoint records survive Orchestrator restarts. The runtime uses
a hard wall-clock process timeout in addition to Cube's idle TTL. Idle expiry
pauses persistent sandboxes rather than losing an uncheckpointed workspace.
Failed checkpoint or provider deletion retains the sandbox and reports an error;
a later run for the same service must wait for cleanup. Only verified uploads
advance the durable checkpoint pointer. Plugin assets remain owned by the
Orchestrator release and are injected per run, on tmpfs. Checkpoints exclude
managed model/Plugin configuration and provider credentials.

## PoC evidence and design review

- The installed S3 driver initially failed mounts because MinIO path-style
  addressing was absent. Added `S3FS_EXTRA_OPTS=-ouse_path_request_style` to the
  `cube-volume-s3` Secret and gracefully restarted Cubelet after confirming all
  seven unrelated sandboxes were paused. No unrelated sandbox was deleted.
- A real mounted volume then failed `git init` while writing `.git/config.lock`.
  Direct S3 FUSE is therefore rejected for repository execution in this deployment.
- A runner template built from release `v0.0.139` with injected envd became READY:
  `tpl-c2e13917ebdf4f02be96e1ac`. Native filesystem Git commit, executable bits and
  symlinks passed. A presigned upload to the real Cloud object store, VM deletion,
  a fresh VM, download/restore and clean Git status all passed. VM creation took
  1.867s and 0.887s in this sample. Both VMs and the PoC object were deleted.
- Review: checkpoint I/O adds startup/cleanup latency. Stream compressed archives
  from the guest directly to S3; do not buffer a workspace in Orchestrator memory.
  Preserve the previous checkpoint until the replacement upload is verified.
- Review: envd can execute root commands and the deployed API does not return an
  envd access token. The unprivileged runner must be blocked from the envd port
  inside its VM; validate this before any production credential injection.

## Implementation review and verification

- Native guest tests passed for non-root execution, IPv4/IPv6 envd denial,
  read-only model and JType configuration, checkpoint upload/readback, restore
  into a fresh guest and process-tree timeout. Root helpers never source task
  environment; task PATH/BASH_ENV/LD_PRELOAD take effect only after privilege drop.
- The source CubeAPI filters metadata after limiting its global inventory. The
  adapter therefore scans a bounded inventory and fails closed at the bound,
  rather than treating a hidden older sandbox as missing.
- Durable registry records, advisory locks, guest start locks, verified
  checkpoint pointers and orphan reaping cover restarts, duplicate requests,
  ambiguous/late allocations and failed cleanup. Host-clock deadlines survive
  a provider pause. No active process may remain when checkpointing begins.
- First cutover uses `RUNTIME_DRAINING=1` on the existing Kubernetes backend;
  active work completes before checkpoint seeding and the provider switch.
- Existing workspace restore verification passed against real Cube VMs:
  `jtype`: 38,209,137 archive bytes; SHA-256
  `52b273300213e8a150c166df626d7fa3d562b4f1b1e2a628793c8ba0de73c0ce`.
  `jcode`: 167,599,948 archive bytes; SHA-256
  `1d17275fa83902a48ca22e36535a34b8ce00761ea91f0b36ebb5a237b03125fe`.
  Source and restored file bytes, symlinks and modes matched. Restores explicitly
  preserve permissions while normalizing ownership to the fixed runner UID.
- Large exec stdout copies were observed to truncate with exit code 0. Backup
  transfer now compares against a completed source archive and validates gzip;
  loopback HTTP Range transfer was verified for the larger workspace.
- The feature was rebased onto live release source `a0cbec6` before delivery,
  preserving repository conversation/agent-board changes. New migration versions
  are 0075 and 0076; a CI test rejects duplicate migration versions.

## Design review gates

- Cube idle TTL is not a job deadline: enforce both explicitly.
- Creation is not execution: persist/adopt identity and use an atomic guest start
  marker before invoking jcode. A failed external call cannot mean success.
- Volume persistence and filesystem semantics must be proven, not inferred from
  an API response. If the installed backend cannot satisfy them, revise this
  design explicitly before implementing another storage mechanism.
- Preserve existing PVCs until backups and destination data have been verified.
- Remove the prewarm DaemonSet, rather than only deleting its pods (which would
  immediately be recreated). Delete only runtime-owned resources.

Sources: [CubeSandbox source](https://github.com/TencentCloud/CubeSandbox),
[custom templates](https://github.com/TencentCloud/CubeSandbox/blob/master/docs/guide/tutorials/bring-your-own-image.md),
[S3 volumes](https://github.com/TencentCloud/CubeSandbox/blob/master/docs/guide/s3-volume.md),
[lifecycle](https://github.com/TencentCloud/CubeSandbox/blob/master/docs/guide/lifecycle.md).
