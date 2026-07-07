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
 * terminal status we close the stream so the browser's EventSource does not
 * auto-reconnect forever (the server closes a terminal stream — per docs
 * 11-api.md §2.3 the client must stop reconnecting).
 */
import { useCallback, useEffect, useReducer, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useApi } from '../api/ApiProvider';
import type { StreamHandle } from '../api/client';
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
  // Bumped by reconnect() to force the subscribe effect to re-run after a fatal
  // stream error (native EventSource does not auto-reconnect on a non-200).
  const [nonce, setNonce] = useState(0);

  // Hold the live StreamHandle so the terminal-status effect can close it
  // without tearing down / re-running the subscribe effect.
  const handleRef = useRef<StreamHandle | null>(null);

  const derivedStatus = wrap.state.derivedStatus;
  const terminal = derivedStatus ? isTerminal(derivedStatus) : false;

  // Mirror derived status into the run cache so the header updates live.
  useEffect(() => {
    if (!derivedStatus) return;
    qc.setQueryData<Run>(qk.run(runId), (prev) =>
      prev && prev.status !== derivedStatus
        ? { ...prev, status: derivedStatus }
        : prev,
    );
    // On terminal status, refetch the authoritative run so late-populated fields
    // (failure_reason / failure_message / finished_at / pr_url) land in the
    // header — the stream's optimistic status patch doesn't carry them.
    if (isTerminal(derivedStatus)) {
      qc.invalidateQueries({ queryKey: qk.run(runId) });
    }
  }, [derivedStatus, runId, qc]);

  // ST-1: the draft PR is opened by the reconciler a moment AFTER the run goes
  // terminal, so the single terminal refetch above races ahead of pr_url and the
  // SSE stream is already closed. Poll the authoritative run a bounded few times
  // until pr_url lands (readonly runs simply never find one and stop). This is
  // what makes the "Draft PR #N" chip appear without a manual reload (finding F11).
  useEffect(() => {
    if (!terminal) return;
    let cancelled = false;
    const timers: ReturnType<typeof setTimeout>[] = [];
    [1000, 2000, 4000, 8000].forEach((delay) => {
      timers.push(
        setTimeout(() => {
          if (cancelled) return;
          if (qc.getQueryData<Run>(qk.run(runId))?.pr_url) return; // already have it
          void qc.invalidateQueries({ queryKey: qk.run(runId) });
        }, delay),
      );
    });
    return () => {
      cancelled = true;
      timers.forEach(clearTimeout);
    };
  }, [terminal, runId, qc]);

  // The draft PR link can also arrive on a later run.status frame carrying
  // pr_url/pr_number (not the full run); patch those onto the cached run too.
  const prURL = wrap.state.prURL;
  const prNumber = wrap.state.prNumber;
  useEffect(() => {
    if (!prURL) return;
    qc.setQueryData<Run>(qk.run(runId), (prev) =>
      prev && prev.pr_url !== prURL
        ? { ...prev, pr_url: prURL, pr_number: prNumber ?? prev.pr_number ?? null }
        : prev,
    );
  }, [prURL, prNumber, runId, qc]);

  // Close the stream once we've observed a terminal status. The server closes a
  // terminal SSE connection; if we leave the EventSource open the browser treats
  // that close as an error and auto-reconnects (~every 3s) forever, re-replaying
  // the run's whole history each time. Closing here stops that loop.
  useEffect(() => {
    if (terminal) {
      handleRef.current?.close();
      handleRef.current = null;
      setPhase('closed');
    }
  }, [terminal]);

  useEffect(() => {
    if (!enabled || !runId) return;
    // Don't (re)open a stream for an already-terminal run — the close effect
    // owns teardown and the server would just replay history and close again.
    if (terminal) return;
    let cancelled = false;
    let handle: StreamHandle | null = null;

    dispatch({ kind: 'reset' });
    setPhase('connecting');

    (async () => {
      // 1. Replay backlog.
      let afterSeq = 0;
      try {
        const backlog = await api.listEvents(runId, 0);
        if (cancelled) return;
        if (backlog.length) {
          dispatch({ kind: 'events', events: backlog });
          // Cursor for the live stream comes straight from the backlog — the
          // reducer/ref is only updated during render, so reading it here (same
          // synchronous tick) would still be 0 and force a full re-replay.
          // Backlog is seq-ascending per the contract; Math.max guards drift.
          afterSeq = backlog.reduce((m, e) => Math.max(m, e.seq), 0);
        }
      } catch {
        // Non-fatal: the stream will replay from 0 anyway.
      }
      if (cancelled) return;

      // 2. Follow live from our cursor.
      handle = api.streamRun(runId, afterSeq, {
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
      handleRef.current = handle;
    })();

    return () => {
      cancelled = true;
      handle?.close();
      if (handleRef.current === handle) handleRef.current = null;
      setPhase('closed');
    };
    // `nonce` re-runs the effect for an explicit reconnect after a fatal error.
    // `terminal` is intentionally read (not a dep) to gate the initial open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, runId, enabled, qc, nonce]);

  // Re-subscribe after a fatal stream error (401/404/SSE-hostile proxy). The
  // native EventSource permanently closed; this opens a fresh one from our
  // current cursor.
  const reconnect = useCallback(() => setNonce((n) => n + 1), []);

  return {
    events: wrap.state.events,
    lastSeq: wrap.state.lastSeq,
    derivedStatus,
    phase: terminal ? 'closed' : phase,
    terminal,
    reconnect,
  };
}
