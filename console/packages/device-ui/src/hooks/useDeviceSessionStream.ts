/*
 * useDeviceSessionStream — subscribes one device session's timeline:
 * replay durable history (GET events), then follow the device-wide SSE stream
 * (session.event = durable, session.delta = ephemeral, device.status =
 * online edge). The device stream has no after_seq/Last-Event-ID, so every
 * (re)open refetches ?after_seq=lastSeq to close the gap — the reducer's
 * seq dedupe makes the overlap harmless.
 */
import { useCallback, useEffect, useReducer, useRef, useState } from 'react';
import { useDeviceApi } from '../api/DeviceApiProvider';
import type { DeviceStreamHandle } from '../api/devices';
import {
  hasSeqGap,
  initialDeviceSessionState,
  reduceDeviceDelta,
  reduceDeviceEvents,
  type DeviceSessionState,
} from '../deviceview/sessionReducer';

export type DeviceStreamPhase = 'connecting' | 'live' | 'error';

interface Wrap {
  state: DeviceSessionState;
}

type Action =
  | { kind: 'events'; events: Parameters<typeof reduceDeviceEvents>[1] }
  | { kind: 'delta'; deltaKind: string; payload: unknown }
  | { kind: 'reset' };

function reducer(s: Wrap, a: Action): Wrap {
  switch (a.kind) {
    case 'events': {
      const next = reduceDeviceEvents(s.state, a.events);
      return next === s.state ? s : { state: next };
    }
    case 'delta': {
      const next = reduceDeviceDelta(s.state, a.deltaKind, a.payload);
      return next === s.state ? s : { state: next };
    }
    case 'reset':
      return { state: initialDeviceSessionState() };
  }
}

export function useDeviceSessionStream(deviceId: string, sessionId: string | undefined) {
  const api = useDeviceApi();
  const [wrap, dispatch] = useReducer(reducer, undefined, () => ({
    state: initialDeviceSessionState(),
  }));
  const [phase, setPhase] = useState<DeviceStreamPhase>('connecting');
  /** Online edge from device.status frames; undefined until the first frame. */
  const [online, setOnline] = useState<boolean | undefined>(undefined);
  const [nonce, setNonce] = useState(0);
  const handleRef = useRef<DeviceStreamHandle | null>(null);
  // Mirror lastSeq into a ref so SSE callbacks (stable closures) can gap-fill.
  const lastSeqRef = useRef(0);
  lastSeqRef.current = wrap.state.lastSeq;

  useEffect(() => {
    if (!deviceId || !sessionId) return;
    let cancelled = false;
    let handle: DeviceStreamHandle | null = null;

    dispatch({ kind: 'reset' });
    setPhase('connecting');

    const refill = async (afterSeq: number) => {
      try {
        const events = await api.listSessionEvents(deviceId, sessionId, afterSeq);
        if (!cancelled && events.length) dispatch({ kind: 'events', events });
      } catch {
        // Non-fatal: the next SSE frame's seq gap triggers another refill.
      }
    };

    (async () => {
      // 1. Replay durable history.
      await refill(0);
      if (cancelled) return;

      // 2. Follow live.
      handle = api.streamDevice(deviceId, {
        onOpen: () => {
          if (cancelled) return;
          setPhase('live');
          // The stream has no resume cursor — close any gap from the downtime.
          refill(lastSeqRef.current);
        },
        onFrame: (frame) => {
          if (cancelled) return;
          if (frame.event === 'device.status') {
            setOnline(frame.data.online === true);
            return;
          }
          if (frame.event === 'session.event' && frame.data.session_id === sessionId) {
            if (hasSeqGap(lastSeqRef.current, frame.data.seq)) {
              refill(lastSeqRef.current);
            }
            dispatch({
              kind: 'events',
              events: [{ seq: frame.data.seq, kind: frame.data.kind, payload: frame.data.payload, ts: new Date().toISOString() }],
            });
            return;
          }
          if (frame.event === 'session.delta' && frame.data.session_id === sessionId) {
            dispatch({ kind: 'delta', deltaKind: frame.data.kind, payload: frame.data.payload });
          }
        },
        onError: () => {
          if (!cancelled) setPhase('error');
        },
      });
      handleRef.current = handle;
    })();

    return () => {
      cancelled = true;
      handle?.close();
      if (handleRef.current === handle) handleRef.current = null;
    };
  }, [api, deviceId, sessionId, nonce]);

  // Explicit reconnect after a fatal stream error (non-200 → EventSource dies).
  const reconnect = useCallback(() => {
    setNonce((n) => n + 1);
  }, []);

  return {
    state: wrap.state,
    online,
    phase,
    reconnect,
  };
}
