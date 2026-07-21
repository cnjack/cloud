/*
 * sessionReducer.ts — pure state for one device session's live view.
 *
 * Same contract as api/eventReducer.ts ("replay then live; events can repeat
 * or arrive out of order") plus the ephemeral side-channel: agent_text deltas
 * accumulate into a streaming bubble that finalizes (local-only) on
 * agent_done, and the durable agent_message (connector-synthesized full text)
 * then supersedes that local copy. Pure so the gap-fill/dedupe/offline rules
 * are unit-testable.
 */
import type { DeviceSessionEvent } from '../api/devices';
import type { DeviceViewEvent } from './types';

export interface FinalizedText {
  /** Synthetic key (negative, descending) — never collides with event seqs. */
  id: number;
  text: string;
}

/**
 * Live token/context counters from the ephemeral token_update delta (M14) —
 * structurally compatible with jcode-ui-core's TokenSnapshot so the product
 * composer's context ring can read it directly.
 */
export interface DeviceTokenSnapshot {
  total_tokens: number;
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens?: number;
  reasoning_tokens?: number;
  cache_write_tokens?: number;
  call_count?: number;
  cache_hit_rate?: number;
  cache_supported?: boolean;
  model_context_limit: number;
}

export interface DeviceSessionState {
  /** Durable events sorted ascending by seq, deduped. */
  events: DeviceViewEvent[];
  /** Highest durable seq seen — the cursor for gap refills. */
  lastSeq: number;
  seen: Set<number>;
  /** Live agent_text accumulator (ephemeral; empty when not streaming). */
  streamingText: string;
  /** Streaming bubbles finalized locally by agent_done, minus the ones since
   * superseded by a durable agent_message (the connector-synthesized replay
   * of the same text — the local copy is dropped to avoid double-render). */
  finalizedText: FinalizedText[];
  /** Derived agent lifecycle: agent_start/task_status running → true. */
  agentRunning: boolean;
  /** Latest token_update counters (null until the first LLM call reports). */
  tokenSnapshot: DeviceTokenSnapshot | null;
  nextLocalId: number;
}

export function initialDeviceSessionState(): DeviceSessionState {
  return {
    events: [],
    lastSeq: 0,
    seen: new Set(),
    streamingText: '',
    finalizedText: [],
    agentRunning: false,
    tokenSnapshot: null,
    nextLocalId: -1,
  };
}

function asRecord(v: unknown): Record<string, unknown> | undefined {
  return v && typeof v === 'object' && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : undefined;
}

/** Merge durable events (backlog, live session.event, or a gap refill). */
export function reduceDeviceEvents(
  state: DeviceSessionState,
  incoming: DeviceSessionEvent | DeviceSessionEvent[],
): DeviceSessionState {
  const batch = Array.isArray(incoming) ? incoming : [incoming];
  const fresh: DeviceViewEvent[] = [];
  const seenInBatch = new Set<number>();
  for (const ev of batch) {
    if (ev == null || typeof ev.seq !== 'number' || !Number.isFinite(ev.seq)) continue;
    if (state.seen.has(ev.seq) || seenInBatch.has(ev.seq)) continue;
    seenInBatch.add(ev.seq);
    fresh.push({ seq: ev.seq, ts: ev.ts, kind: ev.kind, payload: ev.payload });
  }
  if (fresh.length === 0) return state;

  const events = state.events.slice();
  let needsSort = false;
  let tailSeq = events.length ? events[events.length - 1]!.seq : -Infinity;
  for (const ev of fresh) {
    if (ev.seq < tailSeq) needsSort = true;
    else tailSeq = ev.seq;
    events.push(ev);
  }
  if (needsSort) events.sort((a, b) => a.seq - b.seq);

  const seen = new Set(state.seen);
  let lastSeq = state.lastSeq;
  for (const ev of fresh) {
    seen.add(ev.seq);
    if (ev.seq > lastSeq) lastSeq = ev.seq;
  }

  // Derived lifecycle + streaming-bubble finalization.
  let streamingText = state.streamingText;
  let finalizedText = state.finalizedText;
  let agentRunning = state.agentRunning;
  let nextLocalId = state.nextLocalId;
  for (const ev of fresh) {
    const data = asRecord(ev.payload?.data);
    switch (ev.kind) {
      case 'agent_start':
        agentRunning = true;
        // A new run supersedes any dangling partial stream.
        if (streamingText) {
          finalizedText = [...finalizedText, { id: nextLocalId, text: streamingText }];
          nextLocalId -= 1;
          streamingText = '';
        }
        break;
      case 'agent_done':
        agentRunning = false;
        if (streamingText) {
          finalizedText = [...finalizedText, { id: nextLocalId, text: streamingText }];
          nextLocalId -= 1;
          streamingText = '';
        }
        break;
      case 'agent_message': {
        // The connector-synthesized durable copy of the run's assistant text.
        // The same text may already be on screen as a locally-finalized
        // (agent_done) or still-streaming bubble — drop that local copy so
        // the message renders once, from the durable log.
        const text = typeof data?.text === 'string' ? data.text : '';
        if (text) {
          const last = finalizedText[finalizedText.length - 1];
          if (last && last.text === text) {
            finalizedText = finalizedText.slice(0, -1);
          } else if (streamingText === text) {
            streamingText = '';
          }
        }
        break;
      }
      case 'task_status':
        if (typeof data?.status === 'string') agentRunning = data.status === 'running';
        break;
      case 'session_reset':
        streamingText = '';
        break;
    }
  }

  return { events, lastSeq, seen, streamingText, finalizedText, agentRunning, tokenSnapshot: state.tokenSnapshot, nextLocalId };
}

function num(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined;
}

/** Narrow a token_update delta payload (jcode WebTokenData) defensively. */
export function mapTokenSnapshot(payload: unknown): DeviceTokenSnapshot | null {
  const data = asRecord(asRecord(payload)?.data) ?? asRecord(payload);
  if (!data) return null;
  const total = num(data.total_tokens);
  if (total === undefined) return null;
  return {
    total_tokens: total,
    prompt_tokens: num(data.prompt_tokens) ?? 0,
    completion_tokens: num(data.completion_tokens) ?? 0,
    cached_tokens: num(data.cached_tokens),
    reasoning_tokens: num(data.reasoning_tokens),
    cache_write_tokens: num(data.cache_write_tokens),
    call_count: num(data.call_count),
    cache_hit_rate: num(data.cache_hit_rate),
    cache_supported: typeof data.cache_supported === 'boolean' ? data.cache_supported : undefined,
    model_context_limit: num(data.model_context_limit) ?? 0,
  };
}

/** Fold one ephemeral session.delta into the state (agent_text + token_update). */
export function reduceDeviceDelta(
  state: DeviceSessionState,
  kind: string,
  payload: unknown,
): DeviceSessionState {
  if (kind === 'token_update') {
    const tokenSnapshot = mapTokenSnapshot(payload);
    return tokenSnapshot ? { ...state, tokenSnapshot } : state;
  }
  if (kind !== 'agent_text') return state;
  const data = asRecord(asRecord(payload)?.data);
  const text = typeof data?.text === 'string' ? data.text : '';
  if (!text) return state;
  return { ...state, streamingText: state.streamingText + text, agentRunning: true };
}

/**
 * Gap detection for the SSE path: the stream has no after_seq/Last-Event-ID
 * support, so after a reconnect the hook refetches ?after_seq=lastSeq — the
 * reducer's dedupe makes the overlap harmless. Given the server's immediate
 * session.event after we missed N, true means "we're behind, refetch".
 */
export function hasSeqGap(lastSeq: number, incomingSeq: number): boolean {
  return incomingSeq > lastSeq + 1;
}
