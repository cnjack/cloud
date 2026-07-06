# jcode Cloud Agent — local OrbStack deployment (MVP)

Plain kustomize manifests (no Helm) to run the whole jcloud stack on your
local OrbStack Kubernetes cluster (`kubectl config context: orbstack`). Every
image is built locally and loaded via OrbStack's shared Docker image store
(`imagePullPolicy: Never`) — nothing is pushed to a registry.

This directory (`cloud/deploy/`) is self-contained: it does not modify
`cloud/orchestrator/`, `cloud/runner/`, or `cloud/console/` source.

## Architecture map

```
                          ┌─────────────────────────┐
  console (dev, laptop)   │        jcloud ns         │
  localhost:5173  ───┐    │                          │
  (vite dev server)  │    │  ┌────────────────┐      │
                      │    │  │  orchestrator  │      │
  kubectl             └──────▶  Deployment(1)  │      │
  port-forward :8080  │    │  │  Svc :8080     │      │
  (make port-forward) │    │  └───────┬────────┘      │
                      │    │          │ client-go       │
                      │    │          │ (Jobs, in RBAC  │
                      │    │          │  Role/RoleBinding)
                      │    │          ▼                │
                      │    │  ┌────────────────┐        │
                      │    │  │ Job: runner     │───┐    │
                      │    │  │ (per run,       │   │clone│
                      │    │  │  ephemeral)     │   ▼    │
                      │    │  └───────┬────────┘  ┌─────────────┐
                      │    │          │ LLM calls  │  git-seed    │
                      │    │          ▼            │  Deployment  │
                      │    │  ┌────────────────┐   │  Svc git://  │
                      │    │  │    mockllm      │   │  :9418       │
                      │    │  │  Deployment(1)  │   └─────────────┘
                      │    │  │  Svc :8081      │
                      │    │  └────────────────┘
                      │    │
                      │    │  ┌────────────────┐
                      │    │  │   postgres      │◀── orchestrator (DATABASE_URL)
                      │    │  │  Deployment(1)  │
                      │    │  │  + PVC + Svc     │
                      │    │  └────────────────┘
                      │    └─────────────────────────┘
```

| Component | Manifests | Image | Notes |
|---|---|---|---|
| **postgres** | `base/postgres/` | `postgres:16-alpine` (pre-pulled) | Deployment(1) + PVC(1Gi) + Service(ClusterIP:5432) + Secret (creds + full `DATABASE_URL`) |
| **orchestrator** | `base/orchestrator/` | `jcloud/orchestrator:dev` (local build) | Deployment(1) + Service(ClusterIP:8080) + ServiceAccount + Role/RoleBinding + ConfigMap/Secret env. `-migrate-only` initContainer runs migrations before the main container starts. |
| **mockllm** | `base/mockllm/` | `jcloud/mockllm:dev` (local build, own Dockerfile at `deploy/mockllm/Dockerfile`) | Deployment(1) + Service(ClusterIP:8081). Scripted OpenAI-compatible server (source: `cloud/runner/mockllm`, untouched) so runner Jobs can complete an agent loop with no real API key. |
| **git-seed** | `base/gitseed/` | `jcloud/gitseed:dev` (local build, `deploy/gitseed/Dockerfile`) + `alpine/git` for the seed initContainer | Deployment(1) + PVC(256Mi) + Service(ClusterIP `git`:9418). Serves a bare repo (`seed.git`) over the anonymous `git://` protocol so runner Jobs have a real, in-cluster clonable `REPO_URL`. |

Runner Jobs themselves are **not** part of the base manifests — they are
created dynamically by the orchestrator's reconciler (`internal/k8s/client.go`)
whenever a run transitions `queued → scheduling`, using `RUNNER_IMAGE=jcloud/runner:dev`
from `orchestrator-config`.

### Why a git-seed server instead of pointing at localhost?

Runner Job pods run inside the cluster network. A repo living on the OrbStack
**host** (e.g. `file:///Users/you/some-repo` or a `git daemon` bound only to
`localhost` on the Mac) is not directly reachable from inside a pod.

Two ways to fix that:

1. **Ship an in-cluster git server** (what this repo does): a tiny
   `git daemon --export-all` Deployment serving a seeded bare repo at
   `git://git.jcloud.svc.cluster.local/seed.git`. Self-contained, portable to
   any k8s (kind/minikube/a real cluster), no host-networking assumptions.
2. **Alternative: host-served repo.** OrbStack containers can resolve
   `host.orbstack.internal`, which routes to the Mac host. If you run
   `git daemon --reuseaddr --base-path=$HOME/repos --export-all --listen=0.0.0.0`
   directly on your Mac, a Job could reach it at
   `git://host.orbstack.internal/your-repo.git`. This is **not** used here
   because: (a) it's OrbStack-specific (breaks on kind/minikube/CI), (b) it
   depends on a process running on your host outside k8s's lifecycle (nothing
   in `make up`/`make down` manages it), and (c) it complicates the "clean
   teardown, clean rebuild" story this MVP is going for. It's a fine shortcut
   if you specifically want to point a run at a real local checkout on your
   Mac — just set the project's `repo_url` to that `host.orbstack.internal`
   URL instead of the seed repo's.

### Why alpine/git needed a custom image (`jcloud/gitseed:dev`)

`alpine/git` (used for the seed initContainer, which only needs the `git`
client) does **not** bundle `git-daemon` — Alpine splits it into a separate
`git-daemon` apk package that isn't installed by default. `deploy/gitseed/Dockerfile`
is a 3-line image (`FROM alpine:3.20` + `apk add git git-daemon`) built once
locally so the daemon container has what it needs, without depending on
network access from inside a running Pod.

## Environment contract (orchestrator)

`base/orchestrator/configmap.yaml` (non-secret) and `secret.yaml` (secret)
together mirror every variable read by
`cloud/orchestrator/internal/config/config.go` `Load()`, matching
`cloud/orchestrator/.env.example` 1:1. Notable in-cluster values:

| Var | Local dev (compose) | This deployment |
|---|---|---|
| `DATABASE_URL` | `postgres://jcloud:jcloud@localhost:5432/jcloud` | `postgres://jcloud:jcloud@postgres:5432/jcloud` (Service DNS) |
| `RUNNER_IMAGE` | `ghcr.io/cnjack/jcloud-runner:latest` | `jcloud/runner:dev` (local) |
| `ORCH_BASE_URL` | n/a | `http://orchestrator.jcloud.svc.cluster.local:8080` (for future runner→orchestrator callbacks) |
| `MODEL_BASE_URL` | real provider | `http://mockllm.jcloud.svc.cluster.local:8081/v1` |
| `MODEL_API_KEY` | real key | `dummy-mock-key` (mockllm does not validate it) |
| `CONSOLE_TOKEN` | `dev-console-token` | `dev-console-token` (same, for a frictionless local demo) |
| `K8S_NAMESPACE` | n/a | `jcloud` |
| `KUBECONFIG` | path | **unset** — the orchestrator Pod uses in-cluster config via its mounted ServiceAccount token |

**Contract mismatch worth flagging to the e2e agent:** the reconciler
(`internal/reconciler/reconciler.go` `jobEnv`) currently injects only
`RUN_ID, TASK_PROMPT, ORCH_BASE_URL, MODEL_BASE_URL, MODEL_API_KEY, RUN_TOKEN,
REPO_URL, REPO_BRANCH` into the runner Job env. The runner's
`entrypoint.sh`, however, also reads optional `MODEL_NAME` (default
`mock/mock-model`), `RUN_TIMEOUT`, `START_MOCKLLM`, `MOCK_SCENARIO` — none of
which the orchestrator sets today, so the runner falls back to its own
defaults (`mock/mock-model`, `300s`, unset/`0`). That's fine for this smoke
test because mockllm doesn't care what model name it's asked for, but if the
integration work changes `MODEL_NAME` handling or expects the orchestrator to
pass it, the ConfigMap in `base/orchestrator/configmap.yaml` and
`ORCH_BASE_URL` plumbing are the places to extend. Also: as of this writing
`entrypoint.sh` never calls back to `ORCH_BASE_URL`/`RUN_TOKEN` at all (no
`POST /internal/v1/runs/{id}/events|artifact`) — it only prints the diff to
stdout and `/out/diff.patch`. `GET /api/v1/runs/{id}/artifact` therefore 404s
even on a `succeeded` run today. See "What was verified" below.

## First-run walkthrough

Prerequisites: OrbStack running, `kubectl config current-context` = `orbstack`,
namespace `jcloud` exists (`kubectl get ns jcloud`), Docker CLI pointed at the
OrbStack context (it is, by default, once OrbStack is installed).

```sh
cd cloud/deploy

# 1. Build all local images (orchestrator, runner, mockllm, gitseed).
#    Runner build cross-compiles jcode/acpdrive/mockllm from ../../jcode and
#    ./  (see cloud/runner/build.sh) — that source checkout must exist as a
#    sibling of cloud/ (default: /Users/you/workpath/jjj/jcode).
make build

# 2. Apply everything and wait for rollouts.
make up

# 3. In a separate terminal, forward the orchestrator API to your laptop.
make port-forward
# now http://localhost:8080/healthz should return 200

# 4. Point the console dev server at it (uses this default already):
#    cloud/console/.env.example -> VITE_API_PROXY_TARGET=http://localhost:8080
cd ../console && npm run dev
# open http://localhost:5173, log in with CONSOLE_TOKEN=dev-console-token
```

To create a project against the in-cluster seed repo (no external git needed):

```
repo_url = git://git.jcloud.svc.cluster.local/seed.git
```

### Verify without the console (curl)

```sh
TOKEN=dev-console-token
curl -s http://localhost:8080/healthz

PROJECT=$(curl -s -X POST http://localhost:8080/api/v1/projects \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"demo","repo_url":"git://git.jcloud.svc.cluster.local/seed.git","default_branch":"main"}')
echo "$PROJECT"
PROJECT_ID=$(echo "$PROJECT" | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')

curl -s -X POST http://localhost:8080/api/v1/projects/$PROJECT_ID/runs \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"prompt":"Create a file called HELLO.txt with the text hello"}'
```

Or just run the scripted version: `make smoke` (builds its own port-forward,
creates a project+run, polls to a terminal state, prints Job logs, cleans up).

### make seed-repo

`make seed-repo` restarts the `git-seed` Deployment (the seed script is
idempotent — it no-ops if `/srv/git/seed.git` already exists) and clones from
a throwaway debug pod to prove the repo is reachable in-cluster.

## Teardown

```sh
make down       # deletes all Deployments/Services/PVCs/Secrets/ConfigMaps/RBAC
                # created by this manifest set. Does NOT delete the `jcloud`
                # namespace itself (treated as externally-owned/pre-existing;
                # see the note in base/kustomization.yaml).
```

`make restart` = `make down && make up`.

## Troubleshooting

**Pod stuck `ImagePullBackOff` / "image not found"**
All four custom images (`jcloud/orchestrator:dev`, `jcloud/runner:dev`,
`jcloud/mockllm:dev`, `jcloud/gitseed:dev`) use `imagePullPolicy: Never` and
must exist in the **same** Docker context OrbStack's Kubernetes uses (its own
`orbstack` Docker context, shared automatically — no separate `docker load`
needed). Run `docker images | grep jcloud` to confirm all four are present;
if not, `make build` again. If you built on a different `docker context use`
target, switch back and rebuild.

**Orchestrator crash-loops on startup / logs show "missing required env"**
Check `kubectl -n jcloud logs deploy/orchestrator` (or the `migrate` init
container: `kubectl -n jcloud logs deploy/orchestrator -c migrate` /
`kubectl -n jcloud logs -l app.kubernetes.io/name=orchestrator --all-containers`).
`config.Load()` requires `CONSOLE_TOKEN`, `DATABASE_URL`, and (unless
`DISABLE_K8S=1`) `RUNNER_IMAGE` + `ORCH_BASE_URL` — all provided by
`base/orchestrator/configmap.yaml` + `secret.yaml`; if you edited either,
re-`kubectl apply -k .`.

**RBAC denied (`jobs.batch is forbidden`, `pods is forbidden`, etc.)**
The orchestrator ServiceAccount only has a namespace-scoped `Role` (not
`ClusterRole`) granting `jobs: create/get/list/watch/delete`,
`pods: get/list/watch`, `pods/log: get` in `jcloud` — see
`base/orchestrator/rbac.yaml`. If the orchestrator container's `K8S_NAMESPACE`
env doesn't match the namespace the RoleBinding is applied in, every API call
403s. Both must be `jcloud` (they are, in the shipped manifests). Confirm
with: `kubectl -n jcloud auth can-i create jobs --as=system:serviceaccount:jcloud:orchestrator`.

**Migration failures (`-migrate-only` initContainer fails / `Init:Error`)**
`kubectl -n jcloud logs <orchestrator-pod> -c migrate`. Common cause locally:
postgres not yet accepting connections when the initContainer starts —
Kubernetes will keep retrying the initContainer automatically; if it's stuck
for more than ~30s check `kubectl -n jcloud get pods -l app.kubernetes.io/name=postgres`
and `kubectl -n jcloud logs deploy/postgres`. A schema-level migration bug
would instead show a Go error/stack in the migrate container's logs — that's
an `orchestrator/internal/store/migrations` issue, out of scope for this
manifest set.

**git-seed daemon `CrashLoopBackOff` with `git: 'daemon' is not a git command`**
This means something is running the plain `alpine/git` image as the daemon
container instead of `jcloud/gitseed:dev` — check
`base/gitseed/deployment.yaml`'s `git-daemon` container image, and re-run
`make build-gitseed && make up`. (Historical note: this is exactly the bug hit
and fixed while building this manifest set — `alpine/git` ships the `git`
client but not the separate `git-daemon` apk package.)

**Runner Job fails immediately with `git clone failed`**
Almost always the project's `repo_url` is wrong or git-seed isn't Ready yet.
Confirm with `make seed-repo` (clones from a throwaway pod) and
`kubectl -n jcloud get pods -l app.kubernetes.io/name=git-seed`.

**`GET /api/v1/runs/{id}/artifact` returns 404 on a `succeeded` run**
Expected as of this writing (see "Contract mismatch" above): the runner
entrypoint doesn't yet POST the diff back to the orchestrator's
`/internal/v1/runs/{id}/artifact` ingest endpoint — it only writes to stdout
and `/out/diff.patch` inside the (ephemeral) Job pod. Check
`kubectl -n jcloud logs job/jcloud-run-<id>` for the diff between the
`===JCODE_DIFF_BEGIN/END===` markers instead. This will start passing once the
parallel runner/orchestrator integration work wires up the callback.

## What was verified (real OrbStack cluster, context `orbstack`, namespace `jcloud`)

- `make build` — all four images built and present locally:
  `jcloud/orchestrator:dev`, `jcloud/runner:dev`, `jcloud/mockllm:dev`,
  `jcloud/gitseed:dev`.
- `make up` — all four Deployments (`postgres`, `mockllm`, `git-seed`,
  `orchestrator`) reached `1/1 Available`; migration initContainer completed
  successfully.
- `GET /healthz` via `make port-forward` → `200`.
- `make smoke` (full scripted e2e): created a project pointed at
  `git://git.jcloud.svc.cluster.local/seed.git`, created a run, watched the
  orchestrator's reconciler create `jcloud-run-<id>` Job within one tick,
  observed the run walk `queued → scheduling → running → succeeded` in ~5-8s,
  confirmed the Job pod cloned the seed repo, talked to `mockllm`, wrote
  `HELLO_FROM_JCODE.txt`, and printed a non-empty unified diff — matching PRD
  J1/AC-1/AC-4/AC-5/AC-8 at the infra level.
- Cleaned up: deleted the smoke-test project (cascades runs) and the
  completed Job after each run; confirmed `GET /api/v1/projects` returns
  `{"projects":[]}` afterward; confirmed `make down` removes every resource
  this manifest set created **except** the `jcloud` namespace (verified with
  `kubectl get ns jcloud` still `Active` after `make down`).
- `GET /api/v1/runs/{id}/artifact` on the succeeded run → `404` (runner does
  not yet call back to the orchestrator; diff is visible in Job logs only).
  This is the documented, acceptable-for-this-task gap — flagged above for the
  e2e agent to re-check once the runner/orchestrator integration lands.
