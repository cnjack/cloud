/*
 * DeviceApiProvider.tsx — console-side wrapper around @jcloud/device-ui's
 * provider: wires the auth gate's token getter in, so the rest of the app
 * (App.tsx, tests injecting `api`) keeps its existing shape.
 */
import type { ReactNode } from 'react';
import { DeviceApiProvider as DeviceUiApiProvider, type DeviceApi } from '@jcloud/device-ui';
import { useOptionalAuth } from '../auth/AuthProvider';

export function DeviceApiProvider({
  children,
  api,
}: {
  children: ReactNode;
  /** Injectable for tests; defaults to the real HTTP API on the auth token. */
  api?: DeviceApi;
}) {
  const auth = useOptionalAuth();
  return (
    <DeviceUiApiProvider api={api} getToken={auth?.getToken}>
      {children}
    </DeviceUiApiProvider>
  );
}
