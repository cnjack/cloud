# runview

Adapts the Cloud run-event stream to the published `jcode-ui` conversation
contract.

The data path has three pure stages:

1. `eventModel.ts` defensively narrows loose event payloads.
2. `grouping.ts` merges text chunks and pairs tool/permission events.
3. `threadModel.ts` projects the grouped model to `jcode-ui-core` `ThreadItem`s.

`Timeline.tsx` uses the package's headless `Thread` plus its styled `Message`
and `ToolCallCard` components. Two narrow host rows remain because the published
API cannot represent their lossless Cloud contracts yet: `PermissionCard` keeps
arbitrary ACP `option_id` values, and attributed user messages keep the real
multi-user `by` label instead of package `Message`'s hard-coded “You”. Both use
the package's styling/markdown pipeline and can disappear when those package
seams land.

The directory stays independent of pages, API queries, and hooks. The host
provides the runtime and permission callbacks from `RunDetailPage`.

## Public API

Import through `runview/index.ts`:

```ts
import { Timeline, toThreadItems } from '../runview';
import type { PermissionControls, RunViewEvent } from '../runview';
```

SSE replay/deduplication remains in `api/eventReducer.ts`; this layer only sees
the already ordered event list.
