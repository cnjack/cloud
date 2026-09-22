import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useApi } from './ApiProvider';
import { useOptionalAuth } from '../auth/AuthProvider';
import type { AccountProfile } from './types';

export function useAccountProfile() {
  const api = useApi();
  const auth = useOptionalAuth();
  const userID = auth?.me?.is_service ? undefined : auth?.me?.user.id;
  return useQuery({ queryKey: ['account-profile', userID], queryFn: () => api.getAccountProfile(), enabled: !!userID, staleTime: 60_000 });
}

export function useUpdateAccountProfile() {
  const api = useApi();
  const auth = useOptionalAuth();
  const qc = useQueryClient();
  return useMutation({ mutationFn: (profile: AccountProfile) => api.updateAccountProfile(profile), onSuccess: (profile) => {
    qc.setQueryData(['account-profile', auth?.me?.user.id], profile);
    auth?.setDisplayName?.(profile.display_name);
  } });
}
