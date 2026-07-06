# e2e FINDINGS — jcode Cloud Agent MVP

Findings from running the `cloud/e2e/` suite against the live OrbStack cluster
(context `orbstack`, namespace `jcloud`) on the **integrated** build (runner +
orchestrator wired for the event/artifact callback; migration `0002` applied).

Last full run: **63 assertions PASS · 0 FAIL · 0 SKIP** (exit 0), reproduced
twice. See README.md for how to run.

Per the task rules: **no code was changed** in `orchestrator/`, `runner/`, or
`console/`. The items below are either (a) test-environment limitations that the
suite works around and documents, or (b) benign observations — none is a product
bug. No deploy-manifest fix was needed either (the shipped manifests were
correct for the integrated build).

---

## F1 · mockllm produces a FIXED scripted change, independent of the prompt (test-env limitation, not a bug)

**Where:** `cloud/runner/mockllm/main.go` — the `write_file` scenario (the
default) always emits a `write` tool call for `HELLO_FROM_JCODE.txt` with the
constant content `jcode ran headless in a container and wrote this file.`,
regardless of the incoming `TASK_PROMPT`. The mock is intentionally stateless and
prompt-agnostic (`activeScenario()` keys only off `MOCK_SCENARIO`, and the turn
is derived from whether a tool result is already present — never from the prompt
text).

**Impact on PRD J3-S6:** the PRD's J3-S6 assertion says "A's diff contains `A`
and not `B`; B's diff contains `B` and not `A`." With the mock, run A and run B
produce **byte-identical** diffs (both write the same `HELLO_FROM_JCODE.txt`), so
the *diff-content-distinctness* half of J3-S6 is **not checkable** on this stack.

**How the suite handles it (no code change):** `j3.sh` asserts the isolation
invariants that actually prove non-crosstalk and that are independent of the
mock's determinism:
- each run's event log is independently unique/gapless/monotonic from 1
  (`J3-S4`/`J3-S5`);
- neither run's event log mentions the other run's id (`J3-S4`/`J3-S5`);
- each run has its own terminal `run.status(succeeded)` (`J3-S4`/`J3-S5`);
- both artifacts are present and non-empty (`J3-S6`);
- the two runs have **distinct `k8s_job_name`s** — independent worker Jobs
  (`J3-S6`).

These collectively satisfy the J3 success definition ("state, event stream, diff
mutually non-interfering") at the level the mock permits. The literal "diff
contains A vs B" text check is intentionally **not** asserted; it would require a
prompt-sensitive model (a real LLM, or extending the mock to echo the prompt into
the file content — a `runner/mockllm` code change, out of scope for this phase).

**Recommendation (later phase, not this one):** add a mock scenario that writes
the tail of `TASK_PROMPT` into the file, so J3-S6's diff-distinctness can be
asserted verbatim. Tracked as a follow-up, not a blocker.

---

## F2 · `failure_reason` serialises as JSON `null` on non-failed runs (benign)

**Observation:** on a `succeeded` run, `GET /runs/{id}` returns
`"failure_reason": null` (not omitted, not `""`). `Run.FailureReason` is a named
string type with `json:"failure_reason,omitempty"`. Per `11-api.md §1.2` the
`failure_*` fields are only meaningful when `status=="failed"`, and the suite
never asserts `failure_reason` on a non-failed run, so this has no effect on any
assertion.

**Impact:** none. Documented only so a future reader isn't surprised by `null`
vs empty-string. Not a contract violation.

---

## F3 · On a fast clone failure, the run may be observed `running` *after* the runner already reported `run.failure` (benign ordering)

**Observation (J2):** the durable event order for a clone-failure run was:
`run.status(queued)` -> `run.status(scheduling)` -> `run.failure(clone_failed)` ->
`run.status(running)` -> `run.status(failed)` -> `run.failure(...)`. The
`run.status(running)` (seq 4) lands *after* the runner's `run.failure` (seq 3)
because the reconciler briefly observed the pod as Running (the container
started, attempted the clone, and exited) before the next tick flipped it to
`failed`.

**Impact:** none on correctness. The terminal state is `failed` with
`failure_reason=clone_failed` and a human-readable message, exactly as PRD
J2-S3/AC-9 require. The reconciler correctly **preserves** the runner-reported
`clone_failed` classification rather than overwriting it with the cluster-derived
`agent_error` fallback (verified: `failure_reason=clone_failed`, not
`agent_error`). The suite asserts the terminal reason, not the intermediate
status ordering.

---

## F4 · SSE latency measured is a lower bound (transport + buffer), excluding browser paint

**Observation:** the informational latency spot-check (`latency_spotcheck` in
`lib.sh`) live-tags each SSE frame's local receive time and subtracts the
server-stamped `ts`. Typical figures on this stack:
`p50 ~ 25ms, p95 ~ 30-45ms, max < 50ms` — far under the PRD §8 target of
p95 <= 2s.

**Caveat:** this captures the *emit -> client-observes-frame* delay (orchestrator
event append + hub fan-out + port-forward + curl read). It does **not** include
the browser's render/paint step (unmeasurable headless), nor the runner's
internal 500ms/10-event flush batching (that batching sits *before* the `ts`
stamp, so it is not part of what §8 calls "emit -> render"). The measured figure
is a lower bound on the true end-to-end, but with p95 in the tens of
milliseconds there is a ~40-60x margin to the 2s target. This check is
**informational and never gates** the suite.

---

## F5 · J1-S8 "refresh replay" is proven via cold reads, not a live browser reload

**Note (mapping clarification, not a limitation):** PRD J1-S8 is phrased as
"refresh the page -> event timeline and diff fully replay". A headless suite has
no page to refresh, so `j1.sh` proves the *underlying persistence guarantee* a
refresh depends on:
- a **cold** `GET /runs/{id}/events` returns a unique/gapless/monotonic seq set;
- a **cold** `GET /runs/{id}` still reports the terminal `succeeded`;
- a **second, fresh** SSE connection (`after_seq=0`) replays the *exact same seq
  set* as the durable log.

This reads purely from Postgres (events/run) — no in-memory/live-subscription
state involved. The console agent's job is to prove the DOM re-renders from these
same endpoints; the data-layer guarantee is covered here.

---

## Summary

| Item | Nature | Product bug? | Suite action |
|---|---|---|---|
| F1 mockllm fixed diff | test-env limitation | No | J3-S6 asserts isolation invariants, not diff-text distinctness; documented |
| F2 `failure_reason: null` on success | benign serialisation | No | not asserted on non-failed runs |
| F3 `running` after `run.failure` | benign ordering | No | asserts terminal reason, not interleaving |
| F4 latency lower bound | measurement caveat | No | informational only, never gates |
| F5 J1-S8 via cold reads | mapping clarification | No | proves the persistence guarantee a refresh relies on |

No `FAIL`/`SKIP` assertions remain in a green run. No orchestrator/runner/console
code changes were required or made. No deploy-manifest changes were required or
made.
