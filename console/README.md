# jcode Cloud — Console

The web console for **jcode Cloud Agent**: point a project at a git repo,
dispatch a headless run into your own k8s cluster, watch the agent work in real
time, and review the diff it produces. Model and source code never leave your
domain.

This is the product's face. It implements the User Journeys and UI inventory in
[`../docs/10-prd.md`](../docs/10-prd.md) (§5, §6) against the orchestrator API
contract in [`../docs/11-api.md`](../docs/11-api.md).

---

## Quick start

```bash
pnpm install

# Demo mode — no orchestrator / cluster needed. In-memory mock with fake SSE
# run playback. Best way to see the UI immediately.
pnpm run dev:demo        # http://localhost:5173

# Live mode — proxies /api to a running orchestrator (default :8080).
cp .env.example .env.local   # set VITE_CONSOLE_TOKEN + VITE_API_PROXY_TARGET
pnpm run dev
```

Other scripts:

```bash
pnpm run build         # tsc -b && vite build  → dist/
pnpm run test          # vitest run (API client + SSE reducer + diff parser)
pnpm run typecheck     # tsc -b, no emit
pnpm run lint:tokens   # fail if any hex/rgb color leaks outside tokens.css
```

> **pnpm note:** esbuild ships a native binary via a postinstall that pnpm 11
> gates behind approval; it's pre-approved in `pnpm-workspace.yaml`
> (`onlyBuiltDependencies`). If your pnpm still prints `ERR_PNPM_IGNORED_BUILDS`,
> the bundled binary is already functional — `test`/`build` work regardless.

---

## Environment

All console-visible config is `VITE_`-prefixed (baked into the browser bundle,
so **no secrets beyond the single-tenant console token**).

| Var | Default | Purpose |
|---|---|---|
| `VITE_DEMO` | `0` | `1` → in-memory mock client (no network, fake SSE). |
| `VITE_CONSOLE_TOKEN` | — | Bearer token sent on every `/api/v1/*` request (matches orchestrator `CONSOLE_TOKEN`). |
| `VITE_API_PROXY_TARGET` | `http://localhost:8080` | Where the dev vite proxy forwards `/api`. |
| `VITE_DEMO_SEED` | — | `1` → seed a demo project so demo mode isn't a cold start. |
| `VITE_DEMO_SPEED` | `1` | Speeds up mock run playback (higher = faster). |

`.env.demo` (committed) sets `VITE_DEMO=1` for `pnpm run dev:demo`.

---

## Architecture

```
src/
  api/                 ← the ONE seam to the backend
    types.ts           ← contract types, typed verbatim from 11-api.md
    client.ts          ← ApiClient interface + real HTTP impl (fetch)
    mockClient.ts      ← in-memory ApiClient w/ fake SSE run playback
    eventReducer.ts    ← PURE replay-then-live dedupe/order reducer (unit-tested)
    eventModel.ts      ← narrows loose event payloads → typed timeline items
    queries.ts         ← TanStack Query hooks over the ApiClient
    ApiProvider.tsx    ← picks HTTP vs mock from VITE_DEMO; exposes useApi()
    config.ts          ← reads VITE_ env
  hooks/
    useRunStream.ts    ← ties client + reducer: GET backlog then SSE follow,
                         mirrors derived status into the run cache
  components/          ← primitives: Badge, Button, Card, EmptyState, Modal,
                         Toast, Timeline, DiffView, Field, Spinner, States, …
  pages/               ← ProjectsPage, ProjectDetailPage, RunDetailPage, …
  lib/                 ← format.ts (time/id), diff.ts (unified-diff parser)
  styles/
    tokens.css         ← ★ THE design-token layer (see below)
    global.css         ← reset + base element styling
  scripts/lint-tokens.mjs ← grep-enforced "no raw colors outside tokens.css"
```

**Stack:** Vite + React 18 + TypeScript (strict) · react-router · TanStack Query ·
native `EventSource` for SSE. No Tailwind, no Redux — plain CSS Modules so the
token layer is hand-controlled.

### Client / demo duality

`ApiClient` is a single interface. `createHttpClient` talks to the orchestrator;
`createMockClient` runs entirely in memory and scripts a realistic run
lifecycle (`queued → scheduling → running → succeeded|failed`) emitting
sequenced events over time that `streamRun()` replays-then-follows exactly like
the server. A prompt containing "fail" (or a bad repo URL) fails at clone, so J2
(failure + retry) is demoable and e2e-testable without a cluster.

### SSE: replay-then-live, deduped by seq

`GET /runs/{id}/stream` replays events with `seq > after_seq`, then goes live. On
refresh/reconnect we first `GET /events` for the backlog, then open the stream
from our last seq. Overlap is expected. `eventReducer.ts` is the single place
that guarantees **dedupe by seq**, **total order by seq**, and **latest-wins
status** — it's pure and exhaustively unit-tested (`eventReducer.test.ts`).

---

## Where the design tokens live — and the re-skin plan

**Every** color, font, space, radius, and shadow is a CSS custom property in
[`src/styles/tokens.css`](src/styles/tokens.css). Components reference
`var(--…)` only; **no hardcoded hex/rgb anywhere else**. This is enforced:
`pnpm run lint:tokens` greps `src/` for raw color literals outside `tokens.css`
and fails the build if any leak.

The current tokens are an **interim** set derived from the jcode brand
(`jcode-design/design.md`): dark engineering ground, layered panels, `#FF8400`
orange accent, JetBrains Mono for code/identifiers + system sans for body, and
the full status-badge palette (queued/scheduling/running/succeeded/failed/
canceled/blocked).

**The re-skin pass** (a formal Claude Design skin later) is a **single-file token
swap**: replace the values in `tokens.css`. Because the linter guarantees no
component hardcodes a color, and the semantic token names (`--color-accent`,
`--status-running-fg`, `--panel`, …) are decoupled from the raw brand primitives
(`--jc-orange`, `--jc-ink-*`), re-skinning touches neither components nor logic.

---

## Testing

`pnpm run test` runs vitest. Per the quality bar, the load-bearing pure logic has
unit coverage:

- **`eventReducer.test.ts`** — ordering, replay/live dedupe by seq, latest-wins
  status, malformed-event tolerance, referential-equality bail-out.
- **`client.test.ts`** — request shaping, auth headers, list-envelope unwrap,
  nested error-envelope parsing, artifact route + download URL.
- **`mockClient.test.ts`** — full run lifecycle, failure + retry linkage,
  cancel, and replay-then-live streaming feeding the real reducer.
- **`diff.test.ts`** — the unified-diff parser (multi-file, line numbers,
  headerless patches).

Component tests are optional and omitted; behavior is verified end-to-end in
demo mode instead.

---

## Pages (per PRD §6)

1. **Projects list** (`/`) — first-run empty state + create-project modal
   (name, repo URL, default branch).
2. **Project detail** (`/projects/:id`) — runs table (live-updating status
   badges) + "New Run" composer.
3. **Run detail** (`/runs/:id`) — the hero page: status header (badge, timing,
   failure reason/message, Retry/Cancel per state) + tabbed event timeline
   (SSE live-follow, auto-scroll, pause-on-scroll-up) and diff view
   (hand-rolled unified-diff renderer + download).
4. **App shell** — `[J]CODE CLOUD` wordmark, project switcher, minimal top nav.
   Primary ≥1024px, usable at 768px.
