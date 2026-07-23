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
import { useEffect, useMemo, useState } from 'react';
import { useDeviceApi } from '../api/DeviceApiProvider';
import { useDeviceSessions } from '../api/deviceQueries';
import type { DeviceSession } from '../api/devices';

export interface PendingNewSession {
  commandId: string;
  text: string;
  at: number;
}

/** Report an unresolved command rather than guessing a session from a snapshot. */
export const PENDING_SESSION_TIMEOUT_MS = 60_000;
const COMMAND_POLL_MS = 500;

export type PendingNewSessionIssue =
  | 'command_failed'
  | 'missing_session_id'
  | 'timed_out';

function sessionIDFromResult(result: unknown): string | null {
  if (!result || typeof result !== 'object') return null;
  const sessionID = (result as { session_id?: unknown }).session_id;
  return typeof sessionID === 'string' && sessionID.trim() ? sessionID : null;
}

export function usePendingNewSession(deviceId: string) {
  const api = useDeviceApi();
  const [pending, setPending] = useState<PendingNewSession | null>(null);
  const [issue, setIssue] = useState<PendingNewSessionIssue | null>(null);
  const [acknowledgedSessionID, setAcknowledgedSessionID] = useState<string | null>(null);
  const [isRetryingCommandState, setIsRetryingCommandState] = useState(false);
  const sessions = useDeviceSessions(deviceId, pending ? 2_000 : 10_000);

  const markSent = (info: PendingNewSession) => {
    setIssue(null);
    setAcknowledgedSessionID(null);
    setIsRetryingCommandState(false);
    setPending(info);
  };
  const clear = () => {
    setPending(null);
    setIssue(null);
    setAcknowledgedSessionID(null);
    setIsRetryingCommandState(false);
  };

  useEffect(() => {
    if (!pending) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let failures = 0;
    const deadline = pending.at + PENDING_SESSION_TIMEOUT_MS;
    const schedule = (delay: number) => {
      timer = setTimeout(() => void poll(), delay);
    };
    const poll = async () => {
      try {
        const state = await api.getCommandState(deviceId, pending.commandId);
        if (cancelled) return;
        failures = 0;
        setIsRetryingCommandState(false);
        if (state.status === 'acked') {
          const sessionID = sessionIDFromResult(state.result);
          if (!sessionID) {
            setIssue('missing_session_id');
            return;
          }
          setAcknowledgedSessionID(sessionID);
          return;
        }
        if (state.status === 'failed') {
          setIssue('command_failed');
          return;
        }
        if (Date.now() >= deadline) {
          setIssue('timed_out');
          return;
        }
        schedule(COMMAND_POLL_MS);
      } catch {
        if (cancelled) return;
        if (Date.now() >= deadline) {
          setIssue('timed_out');
          return;
        }
        setIsRetryingCommandState(true);
        failures += 1;
        schedule(Math.min(COMMAND_POLL_MS * 2 ** (failures - 1), 4_000));
      }
    };
    void poll();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [api, deviceId, pending]);

  useEffect(() => {
    if (!pending || issue) return;
    const remaining = Math.max(0, pending.at + PENDING_SESSION_TIMEOUT_MS - Date.now());
    const timer = setTimeout(() => setIssue('timed_out'), remaining);
    return () => clearTimeout(timer);
  }, [issue, pending]);

  const found = useMemo((): DeviceSession | null => {
    if (issue || !acknowledgedSessionID || !sessions.data) return null;
    const session = sessions.data.find((candidate) => candidate.session_id === acknowledgedSessionID);
    // Events can arrive before the connector's session metadata upsert. Do not
    // promote that placeholder into a navigable conversation.
    return session?.meta === null ? null : session ?? null;
  }, [acknowledgedSessionID, issue, sessions.data]);

  return { pending, issue, found, markSent, clear, isRetryingCommandState };
}
