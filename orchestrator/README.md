# jcode Cloud Agent — orchestrator

The control-plane server: REST + SSE API, an idempotent reconciler, and a
runner-Job launcher. Go module `github.com/cnjack/jcloud`. The HTTP/SSE contract
is defined in [`../docs/11-api.md`](../docs/11-api.md) — that document is
authoritative; this README covers the event pipeline and how to run the whole
loop locally.

## Quick start

```bash
make pg-up                                   # dev Postgres on :5432
CONSOLE_TOKEN=dev DATABASE_URL=postgres://jcloud:jcloud@localhost:5432/jcloud?sslmode=disable \
  DISABLE_K8S=1 make run                      # API-only (runs queue, don't schedule)
```

`go test ./...` needs no database or cluster. Postgres-backed store tests are
opt-in:

```bash
JCLOUD_PG_DSN=postgres://jcloud:jcloud@localhost:5432/jcloud?sslmode=disable \
  go test ./internal/store/ -run PG -v
```

## Event pipeline

Every run has an append-only event log (`run_events`) that drives the SSE stream
to the console. Events come from two sources:

- **runner** — `agent.text`, `agent.tool_call`, `agent.tool_result` (and,
  optionally, `run.failure`), POSTed to
  `POST /internal/v1/runs/{id}/events` with the per-run `RUN_TOKEN`.
- **orchestrator-internal** — `run.status` on every state transition,
  `run.artifact` when the diff is uploaded, and `run.failure` when the
  reconciler fails a run from cluster state.

### Server-side `seq` allocation (the collision fix)

Each event has a per-run `seq`: a monotonic integer from 1 that the console uses
for ordering, replay (`after_seq`) and dedupe. **The server allocates `seq`, not
the client.**

The original design let the runner and the internal emitters *both* choose
`seq`, deduped first-writer-wins by `(run_id, seq)`. A runner event and an
internal event could pick the same number and the loser was **silently
dropped**. The fix (migration `0002_event_seq_alloc`):

- `seq` is allocated inside a transaction that locks the `runs` row
  (`SELECT … FOR UPDATE`), so concurrent ingest + emission serialize per run and
  the counter is strictly monotonic and gapless.
- The client-supplied number is demoted to a per-**source** idempotency key
  (`client_seq`), unique on `(run_id, source, client_seq)`. Re-sending a runner
  batch is still a safe no-op but no longer competes for the global `seq`.

Store methods:

- `AppendRunnerEvents(runID, events)` — runner ingest: dedupe by
  `(run_id, "runner", client_seq)`, allocate global `seq`, return the stored
  events (with their allocated `seq`) so the handler publishes the right frames.
- `AppendInternalEvent(runID, type, payload)` — one internal event, allocating
  `seq` atomically. Replaces the racy `NextEventSeq`+`AppendEvents` pattern in
  the reconciler and the artifact handler.

The SSE contract to the console is unchanged; only `seq`'s authority moved to
the server. Concurrency is covered by `TestConcurrentIngestOrderingAndUniqueness`
(MemStore) and `TestPGConcurrentIngestNoCollision` (real Postgres).

### SSE auth for browsers

`GET /api/v1/runs/{id}/stream` accepts the console token **either** as a
`Bearer` header (CLI/fetch) **or** as an `?access_token=` query param, because a
browser's native `EventSource` cannot set headers. Only this read-only stream
endpoint allows the query param; every mutating endpoint stays header-only. See
`../docs/11-api.md` §2.3.

## Launchers

The reconciler schedules runners through a `k8s.JobLauncher`. Which
implementation is used is chosen by `JOB_LAUNCHER`:

| `JOB_LAUNCHER` | impl | use |
|---|---|---|
| `kubernetes` (default) | `k8s.Client` | production: one K8s Job per run |
| `process` | `k8s.ProcessLauncher` | local dev / full-loop test: one `docker run` per run |

`ProcessLauncher` reuses the **same `JobSpec`** (name, env, timeout) the
reconciler builds for Kubernetes, so env injection stays faithful. It names each
container after the deterministic Job name, making `CreateJob` idempotent.
Relevant env for process mode:

- `RUNNER_IMAGE` — a locally-available runner image.
- `RUNNER_NETWORK` — docker network to attach (e.g. to reach a mockllm sidecar).
- `RUNNER_DOCKER_ARGS` — extra `docker run` args (space-split), e.g. a seed
  bind-mount and `--add-host=host.docker.internal:host-gateway` so the runner can
  reach the orchestrator on the host.

## Local full-loop test (no Kubernetes)

`../runner/test-integration.sh` exercises the entire pipeline end-to-end using
the `process` launcher:

```
REST create project + run
  → reconciler docker-runs the runner (same env a K8s Job would inject)
  → runner clones, drives jcode against mockllm, STREAMS events back
  → runner uploads the diff artifact
  → run → succeeded
```

Run it:

```bash
make pg-up
cd ../runner && ./test-integration.sh
```

It asserts the run succeeds, the event log carries the runner's
text/tool_call/tool_result plus the internal run.artifact and terminal
run.status, the `seq`s are unique + gapless (proving the collision fix), and the
artifact endpoint returns the scripted diff.

## Runner Job environment

The reconciler injects these into every runner (see `../docs/11-api.md` §6):
`RUN_ID`, `TASK_PROMPT`, `REPO_URL`, `REPO_BRANCH`, `MODEL_BASE_URL`,
`MODEL_API_KEY`, `MODEL_NAME` (default `mock/mock-model`), `ORCH_BASE_URL`,
`RUN_TOKEN`.

> **Deploy note (k8s manifests):** `MODEL_NAME` is a new Job env var. Set it on
> the orchestrator Deployment (defaults to `mock/mock-model`); point it at your
> real provider/model (e.g. `openai/gpt-4o`) alongside `MODEL_BASE_URL` /
> `MODEL_API_KEY`.
