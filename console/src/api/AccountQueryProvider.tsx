import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useEffect, useMemo, type ReactNode } from 'react';
import { useOptionalAuth } from '../auth/AuthProvider';

/** A new principal gets a fresh cache before any private page can render. */
export function AccountQueryProvider({ children }: { children: ReactNode }) {
  const auth = useOptionalAuth();
  const principal = auth?.status === 'ready' && auth.me
    ? `${auth.me.is_service ? 'service' : 'user'}:${auth.me.user.id}` : 'signed-out';
  const client = useMemo(() => new QueryClient({ defaultOptions: { queries: {
    staleTime: 5_000, retry: 1, refetchOnWindowFocus: false,
  } } }), [principal]);
  useEffect(() => () => { client.clear(); }, [client]);
  return <QueryClientProvider key={principal} client={client}>{children}</QueryClientProvider>;
}
