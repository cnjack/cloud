---
name: deploy-company-cluster
description: Deploy or redeploy jcode Cloud (orchestrator + console) to the company Kubernetes cluster (namespace jcode, context wangwenhui@local). Use whenever asked to "deploy", "redeploy", "ship to the cluster/pod", or "push to prod" for this repo.
---

# Deploy jcode Cloud to the company cluster

Ship the current `main` to the on-prem cluster. The pipeline is: **push `main` →
GitHub Actions builds the `ghcr.io` images → `rollout restart` re-pulls them**.
Images are `:latest` with `imagePullPolicy: Always`, so a restart is what
actually rolls the new build.

## Cluster facts (constant)

| Thing | Value |
|---|---|
| kube context | `wangwenhui@local` |
| namespace | `jcode` |
| API server | `https://192.168.10.221:6443` (the shipped kubeconfig says `:443` — wrong; fix once with `kubectl config set-cluster local --server=https://192.168.10.221:6443`) |
| proxy | prepend `NO_PROXY=192.168.10.221 no_proxy=192.168.10.221` to every `kubectl`/`curl` — a local proxy otherwise hijacks the internal subnet |
| deployments | `orchestrator`, `console` (`postgres` is stateful; don't restart it) |
| site | https://cloud.j-code.net (Kong ingress) |
| CI | `.github/workflows/images.yml` — every push to `main` bumps the patch version and pushes `ghcr.io/cnjack/jcloud-{orchestrator,runner,console,mockllm}` `:latest` (+`:<version>` +`:sha-…`), ~2 min |

## Procedure

Set the env once for the shell:
```bash
export NO_PROXY=192.168.10.221 no_proxy=192.168.10.221
CTX="wangwenhui@local"
```

### 1. Commit + push (only if you have local changes)
Conventional commit, then push `main` (this project ships direct-to-main, no PR).
Pushing is what triggers the image build.
```bash
git add <files>
git commit -m "feat(...): ..."
git push origin main
```
If `main` is already at the version you want (nothing to commit), skip to step 2 —
"redeploy" then just re-pulls the current `:latest`.

### 2. Wait for CI to build the images
```bash
RUN=$(gh run list --workflow=images.yml --limit 1 --json databaseId -q '.[0].databaseId')
gh run watch "$RUN" --exit-status    # exits non-zero if the build failed — stop if so
```
Do NOT `rollout restart` before CI is green, or you re-pull the OLD `:latest`.

### 3. (Only if you changed anything under `deploy/`) apply manifests + replay secrets
Skip this for code-only changes. `kubectl apply -k` overwrites the orchestrator
Secret with the base placeholders, so the **real credentials must be replayed
immediately after** (they live only in the gitignored file).
```bash
kubectl --context "$CTX" apply -k deploy/overlays/company
kubectl --context "$CTX" apply -f deploy/secrets/orchestrator-secret.company.local.yaml
```

### 4. Roll the deployments (re-pull `:latest`)
```bash
kubectl --context "$CTX" -n jcode rollout restart deploy/orchestrator deploy/console
kubectl --context "$CTX" -n jcode rollout status deploy/orchestrator --timeout=600s
kubectl --context "$CTX" -n jcode rollout status deploy/console --timeout=600s
```
The orchestrator pod runs an `initContainer: migrate` that applies pending DB
migrations automatically before the app starts — no manual migration step.
The ghcr pull over the cluster's egress can take several minutes; the old pod
keeps serving until the new one is Ready (zero downtime).

### 5. Verify
```bash
POD=$(kubectl get pod -n jcode --context "$CTX" -l app.kubernetes.io/name=orchestrator -o jsonpath='{.items[0].metadata.name}')
kubectl logs -n jcode --context "$CTX" "$POD" -c migrate | tail -1        # "migrations applied"
kubectl get pods -n jcode --context "$CTX"                                 # all 1/1 Running
curl -s -o /dev/null -w "%{http_code}\n" https://cloud.j-code.net          # 200
```
For a schema/API change, also spot-check the relevant `GET /api/v1/system…`
via `kubectl exec … -- wget -qO- --header="Authorization: Bearer $CONSOLE_TOKEN" http://localhost:8080/api/v1/system`.

## Gotchas

- **`kubectl $VAR get …` misparses** ("flags cannot be placed before plugin
  name"). Put flags AFTER the subcommand: `kubectl get pods -n jcode --context …`.
- **Slow first pull**: `:latest` + `imagePullPolicy: Always` re-pulls on every
  restart; the node → ghcr pull can take 5+ min. No `ImagePullBackOff` in the
  events = it's just pulling; wait.
- **Secrets discipline**: real creds only in `deploy/secrets/*.local.yaml`
  (gitignored). Any `apply -k` clobbers them with placeholders → always replay
  (step 3). Code-only deploys skip apply -k entirely and are safe.
- **Don't restart `postgres`** — it's the stateful store.
- **Connectivity check** if kubectl hangs: `nc -vz 192.168.10.221 6443` (should
  be OPEN); `ping` works but the API needs the `NO_PROXY` above when a proxy is set.
