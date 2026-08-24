# Usage ledger: capture, attribution, pricing, and honest presentation

Status: implementation contract for Feature Loop 4 in
[`22-jtype-agent-work-prd.md`](./22-jtype-agent-work-prd.md).

## Product promise

jcode Cloud shows operational model usage that can be traced to the work that
caused it. It does not present the data as a provider invoice and it never turns
missing telemetry into zero.

The ledger answers four separate questions:

1. **What did the provider report?** Input, output, cache read/write tokens and,
   when present, provider-reported cost.
2. **How complete was capture?** `reported`, `partial`, `unavailable`, or
   `parse_error` is visible at Run and aggregate level.
3. **What work owns the usage?** Exactly one primary subject (`run` or
   `device`) plus immutable display dimensions.
4. **How was an estimate produced?** An estimate names an immutable pricing
   revision. Unpriced categories stay `Uncosted`.

The UI always includes:

> Usage is operational telemetry, not a provider invoice.

## Boundary and privacy contract

Capture happens only on the shared `proxyResolvedModel` response path.

- Run proxy requests have primary subject `run`.
- Device Cloud Model Proxy requests have primary subject `device`.
- A request can never produce both subjects. A Run context wins if a future
  route contains both.
- A device calling a local model is outside this path.
- Device Relay session/event envelopes remain opaque ciphertext and are never
  inspected for usage.

The observer may retain at most 64 KiB of parser state. It never persists or
logs prompt text, response text, raw provider JSON/SSE, API keys, authorization
headers, custom provider headers, or device envelopes.

Only the following normalized values may be stored:

- stable request id;
- primary subject and immutable attribution snapshots;
- provider/model identifiers and display snapshots;
- normalized token counts;
- normalized cost in integer micros plus ISO-style currency;
- capture status and bounded error category;
- pricing revision reference;
- timestamps and replacement/version metadata.

Observer, parser, queue, and store failures never change the upstream status,
headers, body bytes, chunk order, or flush behavior. They are logged by category
without response content.

## Capture semantics

### Supported response shapes

The first implementation recognizes OpenAI-compatible usage in:

- non-streaming JSON: a top-level `usage` object;
- SSE: the last valid `data: { ... "usage": ... }` event.

Normalized aliases:

| Normalized value | Accepted provider fields |
| --- | --- |
| input | `input_tokens`, `prompt_tokens` |
| output | `output_tokens`, `completion_tokens` |
| cache read | `input_tokens_details.cached_tokens`, `prompt_tokens_details.cached_tokens`, `cache_read_input_tokens` |
| cache write | `cache_creation_input_tokens`, `cache_write_input_tokens`, detail-level `cache_write_tokens` |
| reported cost | `cost`, `total_cost` on `usage`; currency from `currency`, otherwise no currency is invented |

All counts must be non-negative integers. Cost is converted from a decimal
string to integer micros without binary floating-point arithmetic.

### Capture status

| Status | Meaning |
| --- | --- |
| `reported` | Valid input and output token counts were present. Optional cache/cost fields may also be present. |
| `partial` | A usage object was valid but contained only part of the normalized usage contract. |
| `unavailable` | The response completed successfully with no recognized usage object. |
| `parse_error` | A usage candidate existed but was malformed, exceeded the bounded parser state, or contained invalid values. |

An upstream error response is not rewritten for usage capture. The existing
error normalization contract remains authoritative; no usage event is required
for a request that did not produce a successful model response.

### Idempotency and completion

One stable random request id is created before forwarding. Observer completion
is guarded in memory and the database enforces a unique request id. A duplicate
EOF/Close callback returns the existing event and never increments a rollup
twice.

The final observer callback submits to a bounded asynchronous persistence lane.
When the lane is saturated, Cloud logs `usage_capture_queue_full`; it does not
slow or fail the model response.

## Attribution snapshot

Every event freezes the dimensions needed to explain it later.

For a Run:

- Run, Project, and Service id/name;
- accountable member id/label only when provenance precision permits it;
- visible Automation id/name for SCM, Cron, or Manual execution;
- JType workspace/document/path for a Kanban Card;
- provider/model id/name at request time.

A Cron `create_card` Run is attributed to its originating Cron Automation and
to the generated Card. The hidden Service Kanban rule remains an execution
mechanism, not the user-facing Automation total. An ordinary user-moved Card has
Card attribution but no user-facing Automation dimension.

For a Device Cloud Model Proxy request:

- Cloud user id;
- device id/name;
- model/provider snapshot;
- grant scope kind/id/name (`account`, `project`, or `cluster`).

Snapshots are display/audit data only and never participate in authorization.

## Pricing

A pricing revision is immutable:

- id;
- catalog model id plus provider/model display snapshots;
- currency;
- input, output, cache-read, and cache-write micros per one million tokens;
- effective time;
- creator and creation time.

Cluster Admins may create and list revisions. There is no update or delete API.
Capture chooses the latest revision whose effective time is not after the
request completion time.

Estimated cost is computed per category:

`category tokens × category micros-per-million / 1,000,000`

Rounding is deterministic to the nearest micro. Input tokens reported as a total
are reduced by known cache-read/cache-write tokens before the ordinary input
rate is applied. A category with tokens but without a configured rate remains
Uncosted; priced categories may still show an Estimated subtotal beside it.

Provider-reported cost is shown independently as Reported. Reported and
Estimated are never added together. Different currencies are never combined.

## Storage contract

Migration `0062_usage_ledger.sql` adds:

1. `model_pricing_revisions`: append-only price definitions with immutable
   provider/model snapshots;
2. `usage_events`: append-only normalized raw capture events, unique by request
   id;
3. `usage_request_receipts`: the durable idempotency fence retained with the
   rollup horizon, so replaying a request after raw-event deletion cannot
   increment totals twice;
4. `usage_hourly_rollups`: UTC-hour buckets using the same frozen dimensions.

The event insert and hourly rollup increment happen in one transaction.
Replacement events are explicit rows with `replacement_of` and a higher
version; the initial implementation exposes no mutation endpoint.

Raw events older than 90 days are deleted by the retention job. Hourly rollups
and request receipts default to 365 days. Both horizons are configurable through
`USAGE_RAW_RETENTION` and `USAGE_ROLLUP_RETENTION`; the rollup horizon must be
at least as long as the raw horizon. Rollups survive deletion of Card,
Automation, or Model resources because they contain snapshots rather than
cascading foreign keys. The reconciler runs cleanup at most once per hour.

## API contract

### Summary shape

All summary endpoints return the same shape:

```json
{
  "availability": "available",
  "reason": "",
  "requests": 7,
  "capture": {
    "reported": 5,
    "partial": 1,
    "unavailable": 1,
    "parse_error": 0
  },
  "tokens": {
    "input": 12480,
    "output": 3216,
    "cache_read": 8192,
    "cache_write": null
  },
  "costs": {
    "reported": [{"currency": "USD", "micros": 184200}],
    "estimated": [{
      "currency": "USD",
      "micros": 176100,
      "pricing_revision_ids": ["price_01J..."]
    }],
    "uncosted": [{"category": "cache_write", "tokens": 920}]
  },
  "from": "2026-07-31T00:00:00Z",
  "to": "2026-08-01T00:00:00Z"
}
```

`availability=unavailable` has nullable token values and a reason such as
`no_requests` or `usage_not_reported`; it never returns fabricated zeros.

### Read surfaces

- `GET /api/v1/runs/{id}` adds `usage_summary`.
- `GET /api/v1/projects/{id}/usage?from=&to=&group_by=service|automation|model`
  returns a summary and fixed-order grouped rows.
- `GET /api/v1/repositories/{id}/usage?from=&to=`.
- `GET /api/v1/automations/{aid}/usage?from=&to=`.
- Service Kanban Card execution detail adds `usage_summary`, aggregated from
  the Card's linked Runs.
- `GET /api/v1/account/usage?from=&to=&device_id=&model_id=&grant_scope=`
  contains only Device Cloud Model Proxy usage owned by the caller.

Viewer can read Project/Run/Service/Automation/Card summaries. Account usage is
owner-only by construction. Pagination remains on execution/event lists; usage
summary ranges are limited to the configured rollup retention window. Because
the durable source is an hourly rollup, `from` includes its containing UTC hour
and `to` excludes buckets beginning at or after the supplied instant. The UI
sends explicit UTC instants derived from the viewer's selected range.

### Pricing surfaces

- `GET /api/v1/system/models/{id}/pricing-revisions`
- `POST /api/v1/system/models/{id}/pricing-revisions`

Both require Cluster Admin. POST validates currency, non-negative integer
micros-per-million values, and an explicit effective time.

The Models catalog exposes immutable pricing history and an “Add revision”
dialog. Human-entered currency units per million tokens are converted to
integer micros before submission; an existing revision is never edited in
place.

## UI contract

### Run

The existing Run inspector gains a Usage section below provenance:

- capture badge and request count;
- input/output/cache values;
- Reported, Estimated, and Uncosted as separate rows;
- pricing revision disclosure;
- an unavailable reason instead of `0`.

### Project

Project navigation gains Usage. The page shows:

- range selector in viewer timezone, backed by UTC instants;
- token totals and capture health;
- separate cost source cards;
- grouped table for Service, Automation, and Model;
- links back to Runs/Automations where possible.

### Account and devices

The existing Devices workspace shows Account usage for Device Cloud Model Proxy
requests, grouped by Device, Model, or grant scope with the same range and
capture disclosures. It states that local-device model calls and Device Relay
traffic are outside the total. Account usage never appears in Project totals.

### Card

The JType Card supplement shows aggregate usage beside Cloud executions:

- total across all linked Runs;
- per-Run breakdown and capture state;
- separate costs;
- retained aggregate with “Card unavailable” when the external reference was
  deleted.

On narrow screens identity/provenance precede usage; detailed pricing and
technical request counts collapse below a disclosure.

Aggregated group rows use the latest retained display snapshot for an identity
while their totals include every matching immutable snapshot. A rename cannot
produce duplicate group rows.

## Test plan

Vertical TDD slices:

1. A non-streaming provider response is byte-identical and records normalized
   Run usage once.
2. An SSE response preserves chunk order/timing and records only its final
   usage event.
3. Missing, partial, invalid, duplicate-callback, and parser-limit cases produce
   honest capture states without changing the response.
4. Device capture records user/device/model/grant dimensions and never creates
   Project/Automation dimensions.
5. Immutable pricing revision selection produces category estimates and
   Uncosted gaps without mixing Reported cost.
6. Memory and PostgreSQL contracts prove request-id idempotency, hourly UTC
   rollup, deleted model snapshots, Card/Automation aggregation, and 90-day raw
   retention.
7. API tests prove Viewer read access, Cluster Admin pricing writes, typed
   authorization errors, unavailable-not-zero, and stable grouped ordering.
8. Console tests cover loading, available, partial, unavailable, error,
   keyboard disclosure, and narrow-screen information priority.
9. Existing device E2EE tests continue to prove zero plaintext inspection.
