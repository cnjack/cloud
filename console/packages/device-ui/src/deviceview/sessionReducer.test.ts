/*
 * sessionReducer.test.ts — seq dedupe/ordering, streaming-text accumulation
 * and finalization, derived agent lifecycle, gap detection, online resolution.
 */
import { describe, expect, it } from 'vitest';
import type { DeviceSessionEvent } from '../api/devices';
import {
  hasSeqGap,
  initialDeviceSessionState,
  reduceDeviceDelta,
  reduceDeviceEvents,
} from './sessionReducer';
import { resolveOnline } from './offline';

function ev(seq: number, kind: string, data?: unknown): DeviceSessionEvent {
  return { seq, ts: '2026-07-20T10:00:00Z', kind, payload: { type: kind, data } };
}

describe('reduceDeviceEvents — dedupe + ordering', () => {
  it('dedupes by seq across backlog and live (replay-then-live overlap)', () => {
    let s = initialDeviceSessionState();
    s = reduceDeviceEvents(s, [ev(1, 'agent_start'), ev(2, 'user_message', { content: 'a' })]);
    // A gap refill re-delivers 1-2 plus the missed 3.
    s = reduceDeviceEvents(s, [ev(2, 'user_message', { content: 'a' }), ev(3, 'agent_done', {})]);
    expect(s.events.map((e) => e.seq)).toEqual([1, 2, 3]);
    expect(s.lastSeq).toBe(3);
  });

  it('keeps ascending order for out-of-order arrivals', () => {
    let s = initialDeviceSessionState();
    s = reduceDeviceEvents(s, [ev(3, 'agent_done', {}), ev(1, 'agent_start')]);
    expect(s.events.map((e) => e.seq)).toEqual([1, 3]);
  });

  it('returns the same reference when everything is a duplicate', () => {
    let s = initialDeviceSessionState();
    s = reduceDeviceEvents(s, [ev(1, 'agent_start')]);
    expect(reduceDeviceEvents(s, [ev(1, 'agent_start')])).toBe(s);
  });

  it('drops malformed events without a numeric seq', () => {
    const s = reduceDeviceEvents(initialDeviceSessionState(), [
      { seq: Number.NaN, ts: '', kind: 'agent_start', payload: {} } as DeviceSessionEvent,
    ]);
    expect(s.events).toHaveLength(0);
  });
});

describe('streaming text + lifecycle', () => {
  it('agent_text deltas accumulate; agent_done finalizes the bubble locally', () => {
    let s = initialDeviceSessionState();
    s = reduceDeviceEvents(s, [ev(1, 'agent_start')]);
    s = reduceDeviceDelta(s, 'agent_text', { type: 'agent_text', data: { text: 'Hello' } });
    s = reduceDeviceDelta(s, 'agent_text', { type: 'agent_text', data: { text: ', world' } });
    expect(s.streamingText).toBe('Hello, world');
    expect(s.agentRunning).toBe(true);

    s = reduceDeviceEvents(s, [ev(2, 'agent_done', {})]);
    expect(s.streamingText).toBe('');
    expect(s.finalizedText).toEqual([{ id: -1, text: 'Hello, world' }]);
    expect(s.agentRunning).toBe(false);
  });

  it('a new agent_start finalizes a dangling partial stream', () => {
    let s = initialDeviceSessionState();
    s = reduceDeviceDelta(s, 'agent_text', { data: { text: 'partial' } });
    s = reduceDeviceEvents(s, [ev(1, 'agent_start')]);
    expect(s.streamingText).toBe('');
    expect(s.finalizedText[0]!.text).toBe('partial');
  });

  it('agent_message supersedes the locally finalized bubble (no double render)', () => {
    let s = initialDeviceSessionState();
    s = reduceDeviceDelta(s, 'agent_text', { data: { text: 'Hello, world' } });
    s = reduceDeviceEvents(s, [ev(1, 'agent_done', {})]);
    expect(s.finalizedText).toEqual([{ id: -1, text: 'Hello, world' }]);
    // The durable copy of the same text arrives right after agent_done.
    s = reduceDeviceEvents(s, [ev(2, 'agent_message', { text: 'Hello, world' })]);
    expect(s.finalizedText).toEqual([]);
    expect(s.events.map((e) => e.kind)).toEqual(['agent_done', 'agent_message']);
  });

  it('agent_message clears a still-matching stream; unrelated bubbles stay', () => {
    let s = initialDeviceSessionState();
    s = reduceDeviceDelta(s, 'agent_text', { data: { text: 'streaming' } });
    // agent_message lands before agent_done (out-of-order tolerant).
    s = reduceDeviceEvents(s, [ev(1, 'agent_message', { text: 'streaming' })]);
    expect(s.streamingText).toBe('');
    // A different text supersedes nothing.
    s = reduceDeviceDelta(s, 'agent_text', { data: { text: 'other' } });
    s = reduceDeviceEvents(s, [ev(2, 'agent_done', {})]);
    s = reduceDeviceEvents(s, [ev(3, 'agent_message', { text: 'unrelated' })]);
    expect(s.finalizedText).toEqual([{ id: -1, text: 'other' }]);
  });

  it('task_status drives the running flag; session_reset clears the stream', () => {
    let s = initialDeviceSessionState();
    s = reduceDeviceEvents(s, [ev(1, 'task_status', { status: 'running' })]);
    expect(s.agentRunning).toBe(true);
    s = reduceDeviceDelta(s, 'agent_text', { data: { text: 'x' } });
    s = reduceDeviceEvents(s, [ev(2, 'session_reset', {})]);
    expect(s.streamingText).toBe('');
    s = reduceDeviceEvents(s, [ev(3, 'task_status', { status: 'idle' })]);
    expect(s.agentRunning).toBe(false);
  });

  it('tracks token_update deltas (M14) and ignores empty chunks', () => {
    let s = initialDeviceSessionState();
    // token_update feeds the composer context ring (DeviceTokenSnapshot).
    s = reduceDeviceDelta(s, 'token_update', { data: { total_tokens: 1 } });
    expect(s.tokenSnapshot?.total_tokens).toBe(1);
    // Malformed token payloads and empty text chunks are still no-ops.
    expect(reduceDeviceDelta(s, 'token_update', { data: {} })).toBe(s);
    expect(reduceDeviceDelta(s, 'agent_text', { data: { text: '' } })).toBe(s);
  });
});

describe('hasSeqGap', () => {
  it('detects a missed seq, tolerates contiguous and replayed frames', () => {
    expect(hasSeqGap(5, 6)).toBe(false); // contiguous
    expect(hasSeqGap(5, 5)).toBe(false); // duplicate/replay
    expect(hasSeqGap(5, 8)).toBe(true); // missed 6-7
    expect(hasSeqGap(0, 1)).toBe(false); // first frame
  });
});

describe('resolveOnline (offline logic)', () => {
  it('the live stream edge wins over the polled device list', () => {
    expect(resolveOnline(true, true)).toBe(true);
    expect(resolveOnline(false, true)).toBe(false); // stream says offline
    expect(resolveOnline(true, false)).toBe(true); // stream says back online
    expect(resolveOnline(undefined, false)).toBe(false); // list fallback
    expect(resolveOnline(undefined, undefined)).toBe(true); // unknown → optimistic
  });
});
