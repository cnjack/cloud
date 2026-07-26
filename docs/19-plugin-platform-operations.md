# Plugin platform operations

## Required deployment secret

The Orchestrator requires a base64-encoded 32-byte `JCLOUD_MASTER_KEY`. It
encrypts Provider client secrets, App private keys, webhook secrets, Project
Plugin grants, model credentials, and other control-plane secrets.

Do not configure the former `AUTH_TOKEN_KEY` name. The Plugin platform release
deliberately removes that compatibility alias.

Generate a key:

```bash
openssl rand -base64 32
```

Store it only in the environment-specific, gitignored Kubernetes Secret file.
Do not put it in a ConfigMap, Helm values committed to Git, shell history, or
the task runner.

## First setup

1. Keep the ingress unavailable to untrusted networks.
2. Deploy PostgreSQL and the new Orchestrator.
3. Confirm migrations `0043_plugin_platform`,
   `0044_plugin_store_integrity`, and
   `0045_run_plugin_snapshot_credentials` completed.
4. Open `/setup`.
5. Set the externally reachable Cloud Public URL.
6. In that same unauthenticated screen, choose GitHub, GitLab, or Gitea and
   enter its OAuth client ID and write-only secret. Setup verifies the provider
   origin is reachable before it marks the cluster ready.
7. Sign in. The first authenticated account becomes Cluster Admin.
8. As Cluster Admin, use Connections to test, rotate, enable, or disable
   Provider configuration and configure Project Plugin modes.
9. Only then expose the ingress.

There is no bootstrap token. An exposed unfinished setup can be taken over by
the first visitor.

Unauthenticated setup is accepted only when the user table is empty. If an
upgrade or partial restore has users but no completed `cluster_settings` row,
the API returns `database_recovery_required`; repair the row through the
database recovery procedure instead of reopening first-visitor setup.

## Provider configuration

All Provider and JType control-plane HTTP traffic uses the same guarded
connection policy: generic `HTTP_PROXY`/`HTTPS_PROXY` variables and redirects
are disabled, and the resolved dial address is rejected when it is loopback,
link-local/metadata, unspecified, or multicast. RFC1918 and IPv6 ULA addresses
remain available for self-hosted instances. Only an explicit
`http://localhost`, `127.0.0.1`, or `[::1]` URL enables loopback for local
development. Non-success responses are reported by status only; upstream bodies
are never returned or logged because they may reflect an Authorization header.

A cluster operator may deliberately route this traffic through one trusted
egress proxy by setting `PROVIDER_HTTP_PROXY` and
`PROVIDER_HTTPS_PROXY`. `PROVIDER_NO_PROXY` supports host, suffix, IP, and CIDR
exclusions and should keep cluster-local and self-hosted Provider destinations
direct. The proxy is part of the credential trust boundary: it receives target
host metadata and controls routing, so configure it only at cluster scope.
These variables are not injected into task Runner containers.

Runner network egress is configured independently with `RUNNER_HTTP_PROXY`,
`RUNNER_HTTPS_PROXY`, and `RUNNER_NO_PROXY`. The Orchestrator copies non-empty
values into each new Job as both uppercase and lowercase conventional proxy
variables, so Git clone and Provider CLI traffic use the same cluster-selected
route without baking an address into the runner image. `RUNNER_NO_PROXY` must
keep `.svc`, `.svc.cluster.local`, localhost, and the cluster's service CIDRs
direct so run-scoped model, Plugin credential, and artifact calls never leave
the cluster. A configured cluster runner proxy takes precedence over
Project-level injected environment values with the same keys.

### GitHub

- Instance: github.com only.
- Runtime identity: GitHub App installation token.
- Login identity: minimal GitHub App user authorization.
- Repository access: selected on GitHub's App installation page.
- Configure repository content, pull request, issue, check, status, workflow,
  and release permissions required by enabled Plugin capabilities.
- Do not request organization administration, member administration, or
  repository deletion.

The App private key and webhook secret are write-only in the Cluster UI.
Project Consent uses the actual App Installation permissions returned by a
short-lived token, not only the desired App manifest. If permissions change
between preview and submit, Cloud rejects the digest and requires a new review.

After first-cluster setup, finish GitHub Plugin configuration under **Cluster →
Connections → GitHub**:

1. Copy the GitHub App ID from the App's General page.
2. Generate one private key, download the PEM once, and paste the complete PEM
   into the write-only private-key field.
3. Enter the same webhook secret configured on the GitHub App. If the original
   value is no longer available, rotate it in GitHub and Cloud together.
4. Install the App on the intended account or organization and select the
   repositories that Projects may use.

The OAuth callback is `PUBLIC_URL/auth/callback/github`; the App webhook URL is
`PUBLIC_URL/webhooks/github`. OAuth credentials alone support sign-in but are
not sufficient to list or select GitHub App Installations.

### GitLab

- Supported baseline: 17.11.
- Configure one external instance URL.
- Login and Project Plugin flows reuse the configured OAuth application but use
  distinct state, callback, Consent, and scope requests.
- Project Plugin grants are independent per Cloud Project.
- Use the smallest available scopes that support repository writes, merge
  requests, issues, comments, pipelines, tags, releases, and refresh.
- Current Project Plugin OAuth scopes are exactly `read_user api`.

GitLab's coarse `api` scope can exceed the feature envelope. Consent must state
the actual scope.

### Gitea

- Supported baseline: 1.25.
- Configure one external instance URL.
- Project grants are independent and expose every repository visible to the
  granted external account.
- Repository hooks are managed only while SCM Automations need them.

Some Gitea versions expose coarse OAuth scope controls. Consent must state the
actual scope.

Current Project Plugin OAuth scopes are exactly
`read:user write:repository`.

### JType

- Supported baseline: 0.2.
- Configure one external JType URL.
- JType is available as a Project Plugin but not as a Cloud login Provider.
- Each Project grant selects one workspace and can browse all boards in it.
- The runtime uses the existing JType MCP integration.
- The Project grant requests JType's `full` scope and Consent states that it
  applies to every supported read/write operation in the selected workspace.

## Master-key rotation

Rotation is an offline maintenance operation:

1. Stop Orchestrator replicas so no secret writes can race the operation.
2. Back up PostgreSQL.
3. Run the key-rotation command from `orchestrator/` with the old and new
   base64 keys supplied through protected environment variables or mounted
   files:

   ```bash
   DATABASE_URL=postgres://… \
   OLD_JCLOUD_MASTER_KEY_FILE=/run/secrets/old-master-key \
   NEW_JCLOUD_MASTER_KEY_FILE=/run/secrets/new-master-key \
   go run ./cmd/jcloud-key-rotate
   ```
4. Verify every encrypted row was decrypted and re-encrypted in one database
   transaction.
5. Update the Kubernetes Secret to the new `JCLOUD_MASTER_KEY`.
6. Start Orchestrator and run Provider connection tests.
7. Remove temporary copies of the old and new keys.

If any row cannot be decrypted, the transaction must roll back.

## Configuration changes

Changing a Provider URL, OAuth identity, GitHub App identity, or callback origin
shows an impact preview. On confirmation, all affected Project installations
move to `action_required`. Existing runs finish. New dependent runs stop until
an Owner or Cluster Admin reconnects.

At durable dispatch, a run records only references to append-only Provider
configuration and encrypted grant versions. The control plane resolves those
versions when refreshing the run; it never combines an old grant with the
current Provider URL or a later reconnect. OAuth refresh-token rotation updates
the same frozen grant version and synchronizes a live Installation only when it
still points at that version. The runner receives only a short-lived access
token, never either version's refresh token, client secret, or App private key.

Starting a reconnect clears the live access token, refresh token, and expiry
before authorization starts; JType also clears the selected workspace. An
incomplete JType `action_required` Installation may list workspaces only for
the initial selection state (empty workspace, no health error, and current
enabled Provider revision). Any other `action_required` state must reconnect.

Changing only capability metadata or rotating the webhook secret advances the
Provider revision without invalidating healthy grants; their revision is
updated transactionally. Disabling Plugin capability always requires Project
reconnect before it can be used again.

Do not change a Gitea OAuth redirect URI through an API PATCH. Gitea may rotate
the client secret. Use the Provider UI.

## Failure and cleanup

- A failed webhook is visible in the Automation and receipt views.
- There is no Replay and no automatic processing retry.
- Normalized webhook receipts expire after 30 days.
- Uninstall permanently deletes dependent Services and Automations.
- If Provider hook cleanup fails, the installation remains `uninstalling`.
- Force local uninstall can leave a Provider hook behind and requires an
  explicit warning confirmation.
- If every login Provider is broken, recovery requires direct PostgreSQL access.

## Release checklist

1. Back up PostgreSQL.
2. Build Orchestrator, Console, and Runner from the same commit.
3. Verify pinned `gh`, `glab`, and `tea` checksums during the Orchestrator build.
4. Run Go, Console, runner, migration, Kustomize render, and end-to-end tests.
5. Deploy Orchestrator before Console.
6. Verify migration history and readiness.
7. Deploy the Runner image and sync prewarm; the DaemonSet pins both Runner and
   `PLUGIN_RUNTIME_IMAGE` on every eligible node.
8. Deploy Console.
9. Complete first setup before exposing ingress.
10. Test Provider connection, Project Consent, repository selection, one manual
    run, one webhook Automation, one Cron Automation, and one JType board read.

## Kubernetes compatibility

Plugin credentials use a one-shot initializer followed by a normal sync
companion container, both using `PLUGIN_RUNTIME_IMAGE`. Set that variable to the
same immutable Orchestrator release deployed as the control plane. An earlier
Orchestrator-owned init container copies only the Provider CLI/Skill assets in
the run snapshot into a separate tmpfs volume. The Runner writes a completion marker from its `EXIT`
trap; the sync container observes that marker and exits so the Job can finish.
This shape is compatible with the production Kubernetes 1.28 API and does not
require the native-sidecar feature gate. `activeDeadlineSeconds` remains the
bounded fallback when a runner is force-killed before its trap executes.

Runner Pods are required to pass these security checks:

- UID/GID and fsGroup `10001`; `runAsNonRoot: true`;
- `automountServiceAccountToken: false`;
- `allowPrivilegeEscalation: false`;
- every Linux capability dropped;
- `seccompProfile: RuntimeDefault`.

The generic Runner image must not contain `gh`, `glab`, `tea`, any Provider
Skill, or the Provider credential writer. It mounts the Orchestrator-injected
runtime at `/run/jcloud/runtime` and the injected Skills at
`$HOME/.jcode/skills/<provider>`, both read-only. Each managed Provider uses an
independent subPath mount, so unrelated user Skills remain visible when a
persistent HOME PVC is present.
The local `JOB_LAUNCHER=process` development path fails closed for runs with
Project Plugins; runtime injection is supported by the Kubernetes launcher.
