/*
 * useRunStream — subscribes a run's event timeline: replay historical events
 * (GET /events) then follow live (SSE), feeding both into the pure reducer.
 *
 * Flow (matches AC-7 "refresh/reconnect can fully replay"):
 *   1. fetch backlog via listEvents(after_seq=0)  → seeds the reducer
 *   2. open streamRun(after_seq=lastSeq)           → server replays gap + live
 *   3. reducer dedupes by seq, so overlap between (1) and (2) is harmless
 *
 * The reducer's derivedStatus is mirrored into the run-detail query cache so the
 * status header advances live without a separate poll. When the run reaches a
 * terminal status we close the stream.
 */
import { useEffect, useReducer, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useApi } from '../api/ApiProvider';
import { qk } from '../api/queries';
import {
  initialEventState,
  reduceEvents,
  type EventState,
} from '../api/eventReducer';
import { isTerminal, type Run, type RunEvent } from '../api/types';

export type StreamPhase = 'connecting' | 'live' | 'closed' | 'error';

interface StreamStateWrap {
  state: EventState;
}

type Action =
  | { kind: 'events'; events: RunEvent[] }
  | { kind: 'reset' };

function reducer(s: StreamStateWrap, a: Action): StreamStateWrap {
  switch (a.kind) {
    case 'events': {
      const next = reduceEvents(s.state, a.events);
      return next === s.state ? s : { state: next };
    }
    case 'reset':
      return { state: initialEventState() };
  }
}

export function useRunStream(runId: string, enabled = true) {
  const api = useApi();
  const qc = useQueryClient();
  const [wrap, dispatch] = useReducer(reducer, undefined, () => ({
    state: initialEventState(),
  }));
  const [phase, setPhase] = useState<StreamPhase>('connecting');

  // Keep the latest lastSeq in a ref so the effect can read it without
  // re-subscribing on every event.
  const lastSeqRef = useRef(0);
  lastSeqRef.current = wrap.state.lastSeq;

  const derivedStatus = wrap.state.derivedStatus;

  // Mirror derived status into the run cache so the header updates live.
  useEffect(() => {
    if (!derivedStatus) return;
    qc.setQueryData<Run>(qk.run(runId), (prev) =>
      prev && prev.status !== derivedStatus
        ? { ...prev, status: derivedStatus }
        : prev,
    );
    // On terminal status, refetch the authoritative run so late-populated fields
    // (failure_reason / failure_message / finished_at / mr_url) land in the
    // header — the stream's optimistic status patch doesn't carry them.
    if (isTerminal(derivedStatus)) {
      qc.invalidateQueries({ queryKey: qk.run(runId) });
    }
  }, [derivedStatus, runId, qc]);

  useEffect(() => {
    if (!enabled || !runId) return;
    let cancelled = false;
    let handle: { close: () => void } | null = null;

    dispatch({ kind: 'reset' });
    lastSeqRef.current = 0;
    setPhase('connecting');

    (async () => {
      // 1. Replay backlog.
      try {
        const backlog = await api.listEvents(runId, 0);
        if (cancelled) return;
        if (backlog.length) dispatch({ kind: 'events', events: backlog });
      } catch {
        // Non-fatal: the stream will replay from 0 anyway.
      }
      if (cancelled) return;

      // 2. Follow live from our cursor.
      handle = api.streamRun(runId, lastSeqRef.current, {
        onOpen: () => !cancelled && setPhase('live'),
        onFrame: (frame) => {
          if (cancelled) return;
          dispatch({ kind: 'events', events: [frame.data] });
          // run.status frames may carry the full run object.
          const maybeRun = (frame.data as { run?: Run }).run;
          if (maybeRun) qc.setQueryData(qk.run(runId), maybeRun);
        },
        onError: () => {
          if (!cancelled) setPhase('error');
        },
      });
    })();

    return () => {
      cancelled = true;
      handle?.close();
      setPhase('closed');
    };
  }, [api, runId, enabled, qc]);

  // Close the stream once we've observed a terminal status.
  const terminal = derivedStatus ? isTerminal(derivedStatus) : false;

  return {
    events: wrap.state.events,
    lastSeq: wrap.state.lastSeq,
    derivedStatus,
    phase: terminal ? 'closed' : phase,
    terminal,
  };
}
