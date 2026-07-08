# runview

Renders a jcode run's event timeline: the event → view-model mapping
(`eventModel.ts`), the streaming/tool-call grouping projection
(`grouping.ts`), and the `Timeline` component + its row renderers
(`Timeline.tsx`, `ToolCard.tsx`, `MessageBlock.tsx`, `StatusPill.tsx`,
`Collapsible.tsx`).

## Why this is its own directory

This is the boundary for a future standalone package: the same event-stream
rendering is expected to be shared with the jcode desktop and web clients, not
just this console. To keep that extraction cheap later, the rule going
forward is:

**Nothing in `runview/` may import from `pages/`, `hooks/`, `api/queries`, or
any other application-specific module of this console.** It depends only on
React, generic libraries (`react-markdown`, `remark-gfm`), and its own files.
Where the host's richer types would have been convenient (e.g. the console's
`RunEvent`/`RunStatus` from `api/types.ts`), runview defines its own minimal,
structurally-compatible equivalents instead (see `types.ts`) — a host passes
its own event objects straight through with no adapter, but runview itself
never references the host's type module.

Visual styling reads the host's `--color-*` / `--status-*` / `--font-*`
design tokens (declared in `styles/tokens.css`) by CSS custom-property name
only — that's a styling contract, not a code dependency. A future host just
needs to define those custom properties; it doesn't need this console's
components.

## Public API

Import everything from `runview` (this directory's `index.ts`), not from its
internal files directly:

```ts
import { Timeline, groupTimeline, toTimelineItem, terminalStatusSeq } from '../runview';
import type { RunViewEvent, GroupedTimelineItem, ToolCardItem } from '../runview';
```

## What's NOT here

`eventReducer.ts` (SSE replay dedupe/sort/derived-status) stays in
`api/eventReducer.ts`. It's data-layer plumbing tied to this console's stream
contract (`pr_url`/`pr_number` derivation, etc.), not a rendering concern —
runview only ever receives the already-deduped, seq-ordered event list.
