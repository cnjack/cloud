/*
 * usePendingNewSession — welcome-page "session is being created" tracking.
 *
 * A send to session 'new' returns 202 before the device has created (let alone
 * mirrored) the session, so the list only shows it seconds later. Without this
 * hook the user got zero feedback: no pending row, no navigation — the new
 * session just popped into the list whenever the 10s poll noticed it. Now the
 * page can show a pending card immediately and auto-open the session as soon
 * as the relay mirrors it (with a faster poll while waiting).
 */
import { useEffect, useMemo, useRef, useState } from 'react';
import { useDeviceSessions } from '../api/deviceQueries';
import type { DeviceSession } from '../api/devices';

export interface PendingNewSession {
  text: string;
  at: number;
}

/** Give up waiting and let the user open the session manually. */
export const PENDING_SESSION_TIMEOUT_MS = 60_000;
/** Clock skew allowance when matching the mirrored session's updated_at. */
const SKEW_MS = 10_000;

export function usePendingNewSession(deviceId: string) {
  const [pending, setPending] = useState<PendingNewSession | null>(null);
  const [expired, setExpired] = useState(false);
  const sessions = useDeviceSessions(deviceId, pending ? 2_000 : 10_000);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const markSent = (info: { text: string; at: number }) => {
    setExpired(false);
    setPending({ text: info.text, at: info.at });
  };
  const clear = () => setPending(null);

  useEffect(() => {
    if (!pending) return;
    timer.current = setTimeout(() => {
      setExpired(true);
      setPending(null);
    }, PENDING_SESSION_TIMEOUT_MS);
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, [pending]);

  const found = useMemo((): DeviceSession | null => {
    if (!pending || !sessions.data) return null;
    // The mirrored session is updated at/after the send; newest first wins.
    const candidates = sessions.data
      .filter((s) => Date.parse(s.updated_at) >= pending.at - SKEW_MS)
      .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
    return candidates[0] ?? null;
  }, [pending, sessions.data]);

  return { pending, expired, found, markSent, clear };
}
