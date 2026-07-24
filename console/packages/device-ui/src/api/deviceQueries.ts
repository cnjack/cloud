/*
 * deviceQueries.ts — TanStack Query hooks over the device relay DeviceApi.
 * Mirrors queries.ts conventions (centralised keys, invalidation on mutation).
 * The sessions list polls (10s) as a freshness backstop alongside the SSE
 * stream — the stream carries events, not session-meta/status rollups.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useDeviceApi } from './DeviceApiProvider';
import type { DeviceSession, SendMessageExtras } from './devices';

export const dqk = {
  devices: ['devices'] as const,
  deviceSessions: (deviceId: string) => ['device-sessions', deviceId] as const,
};

/** Stable client-side defense for older or non-conforming relay implementations. */
export function sortDeviceSessions(sessions: DeviceSession[]): DeviceSession[] {
  return [...sessions].sort((left, right) => {
    const leftAt = left.last_activity_at ? Date.parse(left.last_activity_at) : Number.NaN;
    const rightAt = right.last_activity_at ? Date.parse(right.last_activity_at) : Number.NaN;
    const leftKnown = Number.isFinite(leftAt);
    const rightKnown = Number.isFinite(rightAt);
    if (leftKnown && rightKnown && leftAt !== rightAt) return rightAt - leftAt;
    if (leftKnown !== rightKnown) return leftKnown ? -1 : 1;
    return left.session_id.localeCompare(right.session_id);
  });
}

export function useDevices() {
  const api = useDeviceApi();
  return useQuery({ queryKey: dqk.devices, queryFn: () => api.listDevices() });
}

export function useDeviceSessions(deviceId: string, refetchInterval = 10_000) {
  const api = useDeviceApi();
  return useQuery({
    queryKey: dqk.deviceSessions(deviceId),
    queryFn: () => api.listSessions(deviceId),
    select: sortDeviceSessions,
    enabled: !!deviceId,
    // Backstop polling: SSE delivers events but not the session list rollup.
    // Callers pass a shorter interval while awaiting a just-created session.
    refetchInterval,
  });
}

export function useSendDeviceMessage(deviceId: string) {
  const api = useDeviceApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ sessionId, text, mode, extras }: { sessionId: string; text: string; mode?: string; extras?: SendMessageExtras }) =>
      api.sendMessage(deviceId, sessionId, text, mode, extras),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: dqk.deviceSessions(deviceId) });
    },
  });
}

export function useStopDeviceSession(deviceId: string) {
  const api = useDeviceApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (sessionId: string) => api.stopSession(deviceId, sessionId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: dqk.deviceSessions(deviceId) });
    },
  });
}

/** M16: delete (soft-revoke) a device; the device list refetches afterwards. */
export function useDeleteDevice() {
  const api = useDeviceApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (deviceId: string) => api.deleteDevice(deviceId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: dqk.devices });
    },
  });
}

export function useRespondDeviceApproval(deviceId: string, sessionId: string) {
  const api = useDeviceApi();
  return useMutation({
    mutationFn: ({ approvalId, decision }: { approvalId: string; decision: string }) =>
      api.respondApproval(deviceId, sessionId, approvalId, decision),
  });
}
