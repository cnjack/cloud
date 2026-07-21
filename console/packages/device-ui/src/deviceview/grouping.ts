/*
 * grouping.ts — project the durable event list to render items: map each
 * event, then fold tool_result events into their tool_call card (paired by
 * tool_call_id). Orphan results (no call seen — replay window, missing id)
 * degrade to a standalone failed/succeeded card rather than vanishing.
 */
import { applyToolResult, mapDeviceEvent } from './eventModel';
import type { DeviceToolCardItem, DeviceViewEvent, DeviceViewItem } from './types';

function asRecord(v: unknown): Record<string, unknown> | undefined {
  return v && typeof v === 'object' && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : undefined;
}

export function groupDeviceEvents(events: DeviceViewEvent[]): DeviceViewItem[] {
  const items: DeviceViewItem[] = [];
  /** callId → index into items (tool cards only). */
  const byCallId = new Map<string, number>();
  /** Unmatched tool_call seqs, for id-less pairing fallback. */
  const pendingNoId: number[] = [];

  for (const ev of events) {
    if (ev.kind === 'tool_result') {
      const data = asRecord(ev.payload?.data);
      const callId = typeof data?.tool_call_id === 'string' ? data.tool_call_id : undefined;
      let idx = callId !== undefined ? byCallId.get(callId) : undefined;
      // Fallback: jcode always sends tool_call_id today, but an id-less result
      // pairs with the most recent still-running card instead of duplicating.
      if (idx === undefined && pendingNoId.length > 0) {
        idx = pendingNoId.shift();
      }
      if (idx !== undefined) {
        const card = items[idx] as DeviceToolCardItem;
        items[idx] = applyToolResult(card, ev);
        continue;
      }
      // Orphan result: keep it visible as its own card.
      items.push({
        kind: 'tool_card',
        seq: ev.seq,
        ts: ev.ts,
        tool: typeof data?.name === 'string' ? data.name : 'tool',
        args: '',
        status: typeof data?.error === 'string' ? 'failed' : 'succeeded',
        output:
          (typeof data?.display_output === 'string' && data.display_output) ||
          (typeof data?.output === 'string' && data.output) ||
          (typeof data?.error === 'string' ? data.error : ''),
        resultSeq: ev.seq,
      });
      continue;
    }

    const item = mapDeviceEvent(ev);
    if (!item) continue;
    if (item.kind === 'tool_card') {
      if (item.callId) byCallId.set(item.callId, items.length);
      else pendingNoId.push(items.length);
    }
    items.push(item);
  }
  return items;
}
