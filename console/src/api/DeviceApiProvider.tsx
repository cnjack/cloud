/*
 * DeviceApiProvider.tsx — exposes the device relay DeviceApi (devices.ts) to
 * the tree. Separate from ApiProvider because the device surface is not part
 * of the demo/mock client contract. Tests inject a fake via the `api` prop.
 *
 * The default instance is wrapped in the E2EE layer (encryptedDevices.ts +
 * the shared CEK store) so sessions/events/SSE payloads are decrypted and
 * outgoing messages encrypted transparently, below react-query.
 */
import { createContext, useContext, useMemo } from 'react';
import type { ReactNode } from 'react';
import { useOptionalAuth } from '../auth/AuthProvider';
import { sharedDeviceCrypto } from '../devicecrypto/provider';
import { createDeviceApi, type DeviceApi } from './devices';
import { withDeviceCrypto } from './encryptedDevices';

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
    () => api ?? withDeviceCrypto(createDeviceApi(getToken ?? (() => undefined)), sharedDeviceCrypto),
    [api, getToken],
  );
  return <DeviceApiContext.Provider value={value}>{children}</DeviceApiContext.Provider>;
}

export function useDeviceApi(): DeviceApi {
  const ctx = useContext(DeviceApiContext);
  if (!ctx) throw new Error('useDeviceApi must be used within <DeviceApiProvider>');
  return ctx;
}
