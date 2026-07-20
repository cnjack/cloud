/*
 * deviceQueries.ts — TanStack Query hooks over the device relay DeviceApi.
 * Mirrors queries.ts conventions (centralised keys, invalidation on mutation).
 * The sessions list polls (10s) as a freshness backstop alongside the SSE
 * stream — the stream carries events, not session-meta/status rollups.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useDeviceApi } from './DeviceApiProvider';

export const dqk = {
  devices: ['devices'] as const,
  deviceSessions: (deviceId: string) => ['device-sessions', deviceId] as const,
};

export function useDevices() {
  const api = useDeviceApi();
  return useQuery({ queryKey: dqk.devices, queryFn: () => api.listDevices() });
}

export function useDeviceSessions(deviceId: string) {
  const api = useDeviceApi();
  return useQuery({
    queryKey: dqk.deviceSessions(deviceId),
    queryFn: () => api.listSessions(deviceId),
    enabled: !!deviceId,
    // Backstop polling: SSE delivers events but not the session list rollup.
    refetchInterval: 10_000,
  });
}

export function useSendDeviceMessage(deviceId: string) {
  const api = useDeviceApi();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ sessionId, text, mode }: { sessionId: string; text: string; mode?: string }) =>
      api.sendMessage(deviceId, sessionId, text, mode),
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

export function useRespondDeviceApproval(deviceId: string, sessionId: string) {
  const api = useDeviceApi();
  return useMutation({
    mutationFn: ({ approvalId, decision }: { approvalId: string; decision: string }) =>
      api.respondApproval(deviceId, sessionId, approvalId, decision),
  });
}
