# jcloud e2e suite

End-to-end verification for the **jcode Cloud Agent MVP**, run against the
**live** local OrbStack Kubernetes cluster (context `orbstack`, namespace
`jcloud`). It drives the orchestrator's real HTTP + SSE API and asserts the PRD
user journeys **J1 / J2 / J3** (`cloud/docs/10-prd.md` §5) step by step, mapping
every assertion to a PRD step id (`Jx-Sn`). It prints a per-assertion PASS/FAIL
table and exits non-zero on any FAIL.

This is the **cluster-level** counterpart to `cloud/runner/test-integration.sh`
(which proves the same runner<->orchestrator loop *without* k8s, via the
`process` launcher). This suite proves it end-to-end **on real K8s Jobs**.

- **Contract source of truth:** `cloud/docs/11-api.md` (event taxonomy, SSE
  frame format, `failure_reason` enum, `retried_from`, artifact route).
- **Journeys:** `cloud/docs/10-prd.md` §5 (J1-S1..S8, J2-S1..S5, J3-S1..S6).
- **Findings / known limitations:** [`FINDINGS.md`](FINDINGS.md).

---

## Prerequisites

1. **OrbStack running**, and `kubectl config current-context` == `orbstack`.
   The suite refuses to run against any other context (guard in `e2e.sh`,
   mirroring `deploy/Makefile`).
2. **The stack is deployed and Ready** from the *integrated* build:
   ```sh
   make -C ../deploy build     # rebuilds orchestrator/runner/mockllm/gitseed
   make -C ../deploy up        # applies manifests, waits for rollouts
   kubectl -n jcloud rollout restart deploy/orchestrator   # pick up :dev image
   kubectl -n jcloud rollout status  deploy/orchestrator --timeout=120s
   ```
   (Migration `0002_event_seq_alloc` applies automatically at boot via the
   orchestrator's `-migrate-only` initContainer; the Postgres PVC persists
   across redeploys. `e2e.sh` re-verifies `run_events` has the `source` /
   `client_seq` columns during preflight.)
3. **Tools on your PATH:** `bash`, `curl`, `jq`, `kubectl`. (`perl` is used only
   for the informational latency sample; its absence degrades that one check
   gracefully.)

You do **not** need to start `make port-forward` yourself or fetch the token —
`e2e.sh` sets up its own scratch port-forward and reads `CONSOLE_TOKEN` from
`secret/orchestrator-secret`.

---

## Running

```sh
cd cloud/e2e
./e2e.sh
```

Options (env vars):

| Var | Default | Meaning |
|---|---|---|
| `ONLY` | `all` | Run one journey only: `j1` \| `j2` \| `j3` \| `all` |
| `LOCAL_PORT` | `18080` | Local port for the scratch orchestrator port-forward |
| `POLL_TIMEOUT` | `120` | Seconds to wait for a run to reach a terminal state |
| `NO_COLOR` | (unset) | Disable ANSI colour in the table |

Each journey script is also runnable **standalone** (assuming you already have a
port-forward + token):

```sh
BASE=http://127.0.0.1:8080 TOKEN=dev-console-token ./j1.sh
```

The suite is **idempotent and self-cleaning**: on exit it DELETEs every test
project it created (cascading its runs/events/artifacts) and reaps any leftover
runner Jobs. The stack itself is left running. Re-running back-to-back is safe.

---

## What's covered — PRD traceability

Assertion ids in the table below match the PRD step ids exactly. Some PRD steps
are pure-UI (modal render, badge colour) and are the console agent's concern;
this headless suite asserts the **API/SSE contract those steps submit to or read
from**, noted as "(backing contract)".

### J1 · first use (zero -> diff) — `j1.sh`

| PRD step | Assertion(s) | Script |
|---|---|---|
| J1-S1 | `GET /projects` 200 + `projects` is an array (list-page backing) | `j1.sh` |
| J1-S2 | (console modal render — pure UI; its submit is J1-S3) | — |
| J1-S3 | `POST /projects` 201; response has `id`; list now contains the project | `j1.sh` |
| J1-S4 | `POST /projects/{id}/runs` 201; has `run_id`; initial `status=queued` | `j1.sh` |
| J1-S5 | run reaches `running` (or beyond) <= 30s; **live SSE** carries `agent.text` + `agent.tool_call` + `agent.tool_result`; frames > 0; stream seqs unique & monotonic | `j1.sh` |
| J1-S6 | terminal `status=succeeded`; `started_at` & `finished_at` populated; stream carried `run.artifact` + terminal `run.status(succeeded)` | `j1.sh` |
| J1-S7 | `GET /runs/{id}/artifact` 200; content non-empty unified diff (`diff --git`); contains the mock-scripted change (`HELLO_FROM_JCODE.txt`); `?download=1` -> 200 | `j1.sh` |
| J1-S8 | cold `GET /events` seqs unique/gapless/monotonic; cold `GET /run` still `succeeded`; a **fresh** SSE replay yields the identical seq set (persistence, not memory) | `j1.sh` |
| J1-(ST) | Gitea draft-MR — **not in scope** this phase (stretch); not asserted | — |

### J2 · failure visibility (`clone_failed` + retry) — `j2.sh`

| PRD step | Assertion(s) | Script |
|---|---|---|
| J2-S1 | project with unreachable repo `POST /projects` 201; run `POST /runs` 201; run starts `queued`/`scheduling` | `j2.sh` |
| J2-S2 | event stream contains a `run.failure` (clone-stage error) event | `j2.sh` |
| J2-S3 | terminal `status=failed`; `failure_reason` in enum & == `clone_failed`; `failure_message` non-empty & human-readable | `j2.sh` |
| J2-S4 | `POST /runs/{id}/retry` 201; new run id != original; `retried_from` == original; `attempt` == 2 | `j2.sh` |
| J2-S5 | after `PATCH` project repo_url to the good seed repo, retry reaches `succeeded` | `j2.sh` |

### J3 · parallel isolation (two concurrent runs) — `j3.sh`

| PRD step | Assertion(s) | Script |
|---|---|---|
| J3-S1 | project created; run A created | `j3.sh` |
| J3-S2 | run B created; distinct id from A; both in the run list | `j3.sh` |
| J3-S3 | A and B both active (running/scheduling) at the same sampled instant (overlap); both independently reach `succeeded` | `j3.sh` |
| J3-S4 | A's events unique/gapless/monotonic; A's log never mentions B's id; A has its own terminal `succeeded` | `j3.sh` |
| J3-S5 | B's events unique/gapless/monotonic; B's log never mentions A's id; B has its own terminal `succeeded` | `j3.sh` |
| J3-S6 | both artifacts present & non-empty; A and B have **distinct** `k8s_job_name` (independent worker Jobs) | `j3.sh` |

> **J3-S6 diff-distinctness caveat:** the PRD wording "A's diff contains A, not B"
> is **not** asserted because the mock LLM emits a fixed, prompt-independent diff
> (identical for A and B). The suite instead asserts the isolation invariants
> that actually prove non-crosstalk (disjoint event spaces, independent Jobs,
> both artifacts). See [`FINDINGS.md`](FINDINGS.md) F1.

### PRD acceptance criteria touched

J1 -> AC-1/2 (headless clean exit, captured status), AC-4 (Job per run),
AC-5 (state machine), AC-6 (SSE realtime), AC-7 (replay), AC-8 (diff artifact).
J2 -> AC-9 (`failure_reason`+message), AC-10 (`retried_from`).
J3 -> AC-11 (concurrency isolation). Latency spot-check -> AC-6 / §8 target.

---

## SSE latency spot-check (informational, PRD §8)

After the journeys, `e2e.sh` fires one dedicated run and attaches a **live** SSE
consumer, timestamping each frame's local arrival vs its server `ts`, and prints
`min / p50 / p95 / max / mean` in ms. PRD §8 targets **p95 <= 2s**; observed on
this stack is **p95 in the tens of milliseconds** (~40-60x margin). This check is
purely informational and **never gates** the suite (does not affect the exit
code). See [`FINDINGS.md`](FINDINGS.md) F4 for what the figure does/doesn't
include.

---

## Files

| File | Role |
|---|---|
| `e2e.sh` | Orchestrates the whole suite: context guard, preflight, port-forward, token, runs J1/J2/J3, latency check, summary table, teardown. Non-zero exit on any FAIL. |
| `lib.sh` | Shared helpers: assertion recorder (keyed by PRD step id), curl/token wrappers, polling, SSE helpers, latency spot-check, `print_summary`, cleanup registry. |
| `j1.sh` / `j2.sh` / `j3.sh` | The three journeys. Sourced by `e2e.sh` or runnable standalone. |
| `FINDINGS.md` | Known limitations & benign observations (F1-F5). |
| `README.md` | This file. |

---

## Known limitations (short list — full detail in FINDINGS.md)

- **Mock diff is prompt-independent** -> J3-S6 asserts isolation invariants, not
  literal diff-text distinctness (F1).
- **Latency figure is a transport+buffer lower bound**, excludes browser paint
  (F4); informational only.
- **J1-S8 "refresh"** is proven via cold API reads + a fresh SSE replay (the
  data-layer guarantee a refresh relies on), not a live browser reload (F5).
- **Stretch (Gitea draft-MR, J1-ST)** is out of scope this phase and not
  asserted.

## Scope / rules honoured

- Only touches `cloud/e2e/`. No changes to `orchestrator/`, `runner/`,
  `console/`, or `deploy/` were needed or made.
- `kubectl` context `orbstack` only (hard guard; refuses otherwise).
- No `git commit`. Cleans up its own test projects/runs/Jobs; leaves the stack up.
