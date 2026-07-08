/*
 * grouping.ts — pure rendering projection over the (already seq-ordered,
 * deduped) event stream: a "what should a human see as one block" view on top
 * of "what the wire actually sent". Two projections live here:
 *
 *   1. streaming text merge — jcode streams `agent.text` one ACP chunk at a
 *      time; showing each chunk as its own timeline row is the "one word per
 *      line" bug this module fixes. Consecutive agent.text events, with
 *      nothing else interleaved, collapse into a single prose block.
 *   2. tool call/result pairing — `agent.tool_call` / `agent.tool_result`
 *      share a call_id; pairing them into one card (name + status + args +
 *      output) reads far better than two independent raw-JSON rows. An event
 *      that cannot be paired (no call_id, or no matching counterpart within
 *      the current event list) degrades gracefully to the old standalone-row
 *      rendering.
 *
 * groupTimeline() is recomputed from scratch over the full event list on every
 * call — there is no incremental/hidden state — so it is safe to call on every
 * render (e.g. after an out-of-order arrival gets sorted into place upstream)
 * and the result only ever depends on the input array's contents, never on
 * call history.
 */
import { toTimelineItem } from './eventModel';
import type {
  GroupedTimelineItem,
  RunViewEvent,
  TextBlockItem,
  ToolCardItem,
} from './types';

export function groupTimeline(events: RunViewEvent[]): GroupedTimelineItem[] {
  // Defensive: group strictly in seq order regardless of the order the caller
  // handed events in. The reducer upstream already guarantees this, but a
  // pure projection shouldn't silently mis-group if ever fed an unsorted list
  // (e.g. a re-render mid out-of-order-arrival correction).
  const sorted = events.slice().sort((a, b) => a.seq - b.seq);

  const out: GroupedTimelineItem[] = [];
  let openText: TextBlockItem | null = null;
  const openCalls = new Map<string, ToolCardItem>();

  for (const ev of sorted) {
    if (ev.type !== 'agent.text') {
      // Any non-text event breaks a run of merged text chunks.
      openText = null;
    }

    switch (ev.type) {
      case 'agent.text': {
        const item = toTimelineItem(ev);
        if (item.kind !== 'text') break; // unreachable; narrows TS
        if (openText) {
          openText.text += item.text;
          openText.lastSeq = item.seq;
        } else {
          openText = {
            kind: 'text_block',
            seq: item.seq,
            ts: item.ts,
            lastSeq: item.seq,
            text: item.text,
          };
          out.push(openText);
        }
        break;
      }

      case 'agent.tool_call': {
        const item = toTimelineItem(ev);
        if (item.kind !== 'tool_call') break; // unreachable; narrows TS
        if (!item.callId) {
          // No call_id => can never be paired. Degrade to the standalone row.
          out.push(item);
          break;
        }
        const card: ToolCardItem = {
          kind: 'tool_card',
          seq: item.seq,
          ts: item.ts,
          tool: item.tool,
          callId: item.callId,
          status: 'running',
          args: item.args,
          isError: false,
          callSeq: item.seq,
        };
        openCalls.set(item.callId, card);
        out.push(card);
        break;
      }

      case 'agent.tool_result': {
        const item = toTimelineItem(ev);
        if (item.kind !== 'tool_result') break; // unreachable; narrows TS
        const call = item.callId ? openCalls.get(item.callId) : undefined;
        if (!call) {
          // Orphan result — no open call in this event list to pair with
          // (missing call_id, or the matching call never arrived / was
          // already resolved). Degrade to the standalone row.
          out.push(item);
          break;
        }
        call.output = item.output;
        call.isError = item.isError;
        call.status = item.isError ? 'failed' : 'succeeded';
        call.resultSeq = item.seq;
        call.resultTs = item.ts;
        if (item.tool) call.tool = item.tool;
        openCalls.delete(item.callId!);
        break;
      }

      default: {
        const item = toTimelineItem(ev);
        if (item.kind === 'text' || item.kind === 'tool_call' || item.kind === 'tool_result') {
          break; // unreachable given the switch above; satisfies the type checker
        }
        out.push(item);
      }
    }
  }

  return out;
}
