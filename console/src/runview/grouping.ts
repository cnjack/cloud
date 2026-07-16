/*
 * grouping.ts — pure rendering projection over the (already seq-ordered,
 * deduped) event stream: a "what should a human see as one block" view on top
 * of "what the wire actually sent". Two projections live here:
 *
 *   1. streaming text merge — jcode streams `agent.text` one ACP chunk at a
 *      time; showing each chunk as its own timeline row is the "one word per
 *      line" bug this module fixes. Consecutive agent.text events collapse
 *      into a single prose block. Only CONTENT-FLOW events (tool cards,
 *      permission cards, user messages) break the run — system rows
 *      (run.status, run.session, artifact/git/result/failure, …) do NOT:
 *      they are written by a different component than agent.text (the
 *      orchestrator, e.g. parking the run in awaiting_input at turn end,
 *      vs the runner's batched emitter) and can land BETWEEN the chunks of
 *      one message. Breaking there split a single sentence into two bubbles
 *      with a status row wedged mid-message; instead the text merges across
 *      such rows and the row renders after the merged block.
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
  PermissionCardItem,
  RunViewEvent,
  TextBlockItem,
  ToolCardItem,
} from './types';

// Event types that are part of the agent's content flow: a tool card, a
// permission card or a user chat bubble between two prose chunks must render
// BETWEEN those chunks, so they break the streaming text merge. Everything
// else (run.status, run.session, session.finish, run.artifact, run.git,
// run.result, run.failure, unknown types) is a system row and must NOT split
// a message — see the module header.
const TEXT_MERGE_BREAKERS = new Set([
  'agent.tool_call',
  'agent.tool_result',
  'agent.permission_request',
  'agent.permission_resolved',
  'user.message',
]);

function breaksTextMerge(type: string): boolean {
  return TEXT_MERGE_BREAKERS.has(type);
}

export function groupTimeline(events: RunViewEvent[]): GroupedTimelineItem[] {
  // Defensive: group strictly in seq order regardless of the order the caller
  // handed events in. The reducer upstream already guarantees this, but a
  // pure projection shouldn't silently mis-group if ever fed an unsorted list
  // (e.g. a re-render mid out-of-order-arrival correction).
  const sorted = events.slice().sort((a, b) => a.seq - b.seq);

  const out: GroupedTimelineItem[] = [];
  let openText: TextBlockItem | null = null;
  const openCalls = new Map<string, ToolCardItem>();
  // F8b permission request/resolved pairing, keyed by request_id. Unlike
  // openCalls this map is NEVER pruned on resolve: the request event is
  // delivered synchronously and may sit arbitrarily far from (and even
  // interleave oddly with) the tool_call/resolved events, so pairing must be
  // pure request_id keying with no adjacency or lifecycle assumptions.
  const permCards = new Map<string, PermissionCardItem>();

  for (const ev of sorted) {
    if (breaksTextMerge(ev.type)) {
      // A content-flow event between two prose chunks must RENDER between
      // them, so the chunks stay separate bubbles. System rows are excluded:
      // see the module header for the cross-writer interleaving rationale.
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
        // The initial runner event carries a stable ACP kind in `name`
        // (read/edit/execute), while terminal updates may carry the human title
        // instead. Keep the call's machine name for jcode-ui registry dispatch;
        // only let the result fill a genuinely unknown legacy call name.
        if (item.tool && (!call.tool || call.tool === 'tool')) call.tool = item.tool;
        openCalls.delete(item.callId!);
        break;
      }

      case 'agent.permission_request': {
        const item = toTimelineItem(ev);
        if (item.kind !== 'permission_request') break; // unreachable; narrows TS
        if (!item.requestId) {
          // No request_id => can never be resolved/decided. Render nothing
          // actionable rather than a dead card.
          break;
        }
        if (permCards.has(item.requestId)) {
          // At-least-once delivery: a duplicate request event must not spawn a
          // second card (the first one may already be resolved).
          break;
        }
        const card: PermissionCardItem = {
          kind: 'permission_card',
          seq: item.seq,
          ts: item.ts,
          requestId: item.requestId,
          toolCallId: item.toolCallId,
          title: item.title,
          options: item.options,
          status: 'pending',
        };
        permCards.set(item.requestId, card);
        out.push(card);
        break;
      }

      case 'agent.permission_resolved': {
        const item = toTimelineItem(ev);
        if (item.kind !== 'permission_resolved') break; // unreachable; narrows TS
        const card = item.requestId ? permCards.get(item.requestId) : undefined;
        if (!card) {
          // Orphan resolution (the request event never arrived) — degrade to a
          // standalone system row rather than dropping the outcome.
          out.push(item);
          break;
        }
        card.status = 'resolved';
        card.resolvedOptionId = item.optionId;
        card.resolution = item.resolution;
        card.resolvedSeq = item.seq;
        break;
      }

      default: {
        const item = toTimelineItem(ev);
        if (
          item.kind === 'text' ||
          item.kind === 'tool_call' ||
          item.kind === 'tool_result' ||
          item.kind === 'permission_request' ||
          item.kind === 'permission_resolved'
        ) {
          break; // unreachable given the switch above; satisfies the type checker
        }
        out.push(item);
      }
    }
  }

  return out;
}
