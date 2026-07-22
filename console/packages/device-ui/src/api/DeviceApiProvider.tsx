/*
 * DeviceApiProvider.tsx — exposes the device relay DeviceApi (devices.ts) to
 * the tree. Separate from any host auth layer: the host passes a `getToken`
 * source (console: its AuthProvider; mobile: its token store) and, when not
 * same-origin, `options.baseUrl`/`options.credentials`. Tests inject a fake
 * via the `api` prop.
 *
 * The default instance is wrapped in the E2EE layer (encryptedDevices.ts +
 * the shared CEK store) so sessions/events/SSE payloads are decrypted and
 * outgoing messages encrypted transparently, below react-query.
 */
import { createContext, useContext, useMemo } from 'react';
import type { ReactNode } from 'react';
import { sharedDeviceCrypto } from '../devicecrypto/provider';
import type { DeviceCrypto } from '../devicecrypto/provider';
import { createDeviceApi, type DeviceApi, type DeviceApiOptions } from './devices';
import type { TokenSource } from './errors';
import { withDeviceCrypto } from './encryptedDevices';

const DeviceApiContext = createContext<DeviceApi | null>(null);

export function DeviceApiProvider({
  children,
  api,
  getToken,
  options,
  crypto,
}: {
  children: ReactNode;
  /** Injectable for tests; defaults to the real HTTP API on the token source. */
  api?: DeviceApi;
  /** Bearer-token source; undefined getter means "rely on cookies". */
  getToken?: TokenSource;
  /** Cross-origin base URL / credentials overrides (mobile). */
  options?: DeviceApiOptions;
  /** Optional host-specific CEK storage (mobile uses Keychain/Keystore). */
  crypto?: DeviceCrypto;
}) {
  const value = useMemo<DeviceApi>(
    () =>
      api ??
      withDeviceCrypto(
        createDeviceApi(getToken ?? (() => undefined), options),
        crypto ?? sharedDeviceCrypto,
      ),
    [api, getToken, options, crypto],
  );
  return <DeviceApiContext.Provider value={value}>{children}</DeviceApiContext.Provider>;
}

export function useDeviceApi(): DeviceApi {
  const ctx = useContext(DeviceApiContext);
  if (!ctx) throw new Error('useDeviceApi must be used within <DeviceApiProvider>');
  return ctx;
}
