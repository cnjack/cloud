# CubeSandbox runtime

The company runtime uses CubeSandbox at `192.168.10.194`. The Cloud control
plane, Console and PostgreSQL remain in the company Kubernetes cluster. Runtime
templates are published as `jcloud-cube-{default,go-node,python,rust,polyglot}` by
`images.yml`; each contains the corresponding Runner Profile, envd and the
confinement tools. Project Plugins are injected from the Orchestrator release.

Read [the migration design](../../docs/design/cubesandbox-runtime.md) first.

## Release

1. Run the Go suite and PostgreSQL-gated store suite, then push `main`.
2. Wait for the **matching commit's** images workflow, including all five
   `cube-runner` jobs, to pass. Record its release version.
3. Register all five templates (for the first cutover, omit `--apply` until
   workspace migration has been verified):

   ```sh
   python3 deploy/cubesandbox/register-templates.py --release vX.Y.Z --apply
   ```

   The script merges selected ConfigMap keys; it does not replace the ConfigMap
   or touch secrets. It is idempotent for an existing version. The generated
   JSON file records the exact deployed profile/template mapping.
4. Apply any other manifest changes and immediately replay the local secret as
   usual. If `apply -k` was used, reapply the generated runtime merge patch too:

   ```sh
   kubectl patch configmap orchestrator-config -n jcode --context wangwenhui@local \
     --type merge --patch-file deploy/cubesandbox/runtime-config.generated.json
   ```

5. Restart Orchestrator and Console, wait for readiness, verify migrations and
   `/api/v1/system`, then exercise a real Run via `https://cloud.j-code.net`.
   Verify events, artifacts, a subsequent workspace restore and cancellation.

For the **first provider cutover**, deploy the new Orchestrator image with
Deployment environment overrides `JOB_LAUNCHER=kubernetes` and
`RUNTIME_DRAINING=1`. The API remains available while new dispatch and new
archive jobs pause; existing jobs still finish normally. Confirm
`capacity.scheduling_paused=true` and no scheduling/running/awaiting-input Runs
or active archive Jobs. Recheck the source workspace fingerprints, seed the
verified checkpoints, apply the Cube runtime patch, then remove both temporary
Deployment overrides. This avoids assigning an old Kubernetes Run to the new
provider during a rolling deployment. Never force-stop active user work to make
the cutover pass.

The CubeAPI key, when enabled upstream, belongs in the gitignored company Secret
as `CUBE_API_KEY`. The supplied Cube deployment currently accepts LAN control
requests without an API key. The integration never copies Cube credentials to a
task VM. API and envd/proxy connectivity are distinct; the WebUI login URL is not
the control-plane API endpoint.

When the Aliyun mirror is unavailable, manually dispatch `images.yml` with
`publish_aliyun=false`. The default remains dual-registry publication. The release
tag explicitly records mirror availability. Wait for every image job to pass,
register with `--registry ghcr.io/cnjack`, and pin both control-plane Deployments
(including the migration init container) to that release's GHCR digests. Never
point a GHCR-only release at missing Aliyun tags or reuse unrelated templates.

If a registry transport repeatedly fails, `--template-registry` can select a
temporary mirror for Cube build inputs while `--registry` retains the published
Runner image identity in the ConfigMap. Before using it, verify every mirrored
manifest and blob against the published SHA-256 and size. A mirror must serve
the original manifest bytes; rebuilding or recompressing is not an equivalent
release. Keep its endpoint in the build receipt and record the resulting native
artifact digest. Remove a temporary mirror only after all five templates are
ready and their runtime behavior has been verified.

An Orchestrator-only fix can reuse the already verified immutable Runner/template
mapping. Pin the new Orchestrator image separately and keep `RUNNER_IMAGE` and
`RUNNER_PROFILES_JSON` truthful to the templates in use. Template registration is
serial by default, records native build job IDs, and treats CubeAPI 404 as pending
until the native job completes. `--retry-failed` retries one recorded failed build
after its cause has been repaired.

For a slow registry download, pass `--wait-timeout 21600` to observe each native
build for up to six hours (default: 900 seconds). An observation timeout does not
cancel the native build. Keep its generated receipt and rerun with the same
release, registry and output path to resume tracking the existing job. Do not
delete receipts or create replacement builds merely because polling timed out.
Transport failures on read requests are retried three times; HTTP errors remain
visible to the pending/error handling and template mutations are not retried.

## Workspaces and recovery

The installed S3 FUSE volume failed Git filesystem semantics in PoC. Tasks use
native ext4; per-service checkpoints are streamed through presigned object URLs.
`cube_runtime_jobs` records resource identities, and `cube_runtime_workspaces`
holds only the latest verified checkpoint key. Restores run as UID 10001.
Managed model/Plugin configuration is transient and excluded from checkpoints.

Cancel signals the runner process group. The VM is deleted only after a
checkpoint upload and SHA-256 readback succeed. Failed uploads retain the VM,
surface `checkpoint_failed`, and are retried by terminal-job cleanup. A new Run
waits while the previous workspace cleanup remains pending. An unexpected VM
loss before checkpoint completion is an explicit recovery error.

Before first cutover, drain all Runs and archive both existing service PVCs to
object storage. Verify archive hashes and restore into a disposable Cube VM.
Seed `cube_runtime_workspaces` with the verified checkpoint keys. Retain source
PVCs until migration readback is complete. PostgreSQL and unrelated workloads
must not be deleted.

Generate each backup as a **completed file inside the read-only backup Pod**;
validate gzip and compare its source SHA-256 against the downloaded file. Large
`kubectl exec` stdout transfers were observed to truncate despite exit code 0.
The small `transfer.go` helper serves exactly one file on Pod loopback with HTTP
Content-Length and Range support, for an authenticated `kubectl port-forward`.
Remove the temporary server/forward and backup Pods when verification finishes.
`cmd/cubesandbox-poc --archive ... --checkpoint-key ... --expected-fingerprint ...`
uploads an archive, reads it back, restores it as UID 10001 in a disposable VM,
and compares all regular-file bytes, link targets and modes. The fingerprint
command is available through `--print-fingerprint-command`.

After the production smoke, delete the legacy `jcloud-runner-prewarm` DaemonSet
so its pods do not return. Delete legacy run Jobs only by their recorded owner
and only after confirming their Runs are terminal. The new runtime does not use
Kubernetes Jobs, runner service accounts or image-prewarm pods.
