/*
 * DeviceApiProvider.tsx — exposes the device relay DeviceApi (devices.ts) to
 * the tree. Separate from ApiProvider because the device surface is not part
 * of the demo/mock client contract. Tests inject a fake via the `api` prop.
 */
import { createContext, useContext, useMemo } from 'react';
import type { ReactNode } from 'react';
import { useOptionalAuth } from '../auth/AuthProvider';
import { createDeviceApi, type DeviceApi } from './devices';

const DeviceApiContext = createContext<DeviceApi | null>(null);

export function DeviceApiProvider({
  children,
  api,
}: {
  children: ReactNode;
  /** Injectable for tests; defaults to the real HTTP API on the auth token. */
  api?: DeviceApi;
}) {
  const auth = useOptionalAuth();
  const getToken = auth?.getToken;
  const value = useMemo<DeviceApi>(
    () => api ?? createDeviceApi(getToken ?? (() => undefined)),
    [api, getToken],
  );
  return <DeviceApiContext.Provider value={value}>{children}</DeviceApiContext.Provider>;
}

export function useDeviceApi(): DeviceApi {
  const ctx = useContext(DeviceApiContext);
  if (!ctx) throw new Error('useDeviceApi must be used within <DeviceApiProvider>');
  return ctx;
}
