/* Scope-aware provider administration; account and cluster caches stay separate. */
import { useMutation, useQuery, useQueryClient, type UseMutationResult, type UseQueryResult } from '@tanstack/react-query';
import { useApi } from '../../api/ApiProvider';
import { useOptionalAuth } from '../../auth/AuthProvider';
import { qk } from '../../api/queries';
import type { CatalogModel, CreateModelProviderInput, CreateProviderModelInput, ModelProvider, ModelProviderVerification, ProviderModel, UpdateModelInput, UpdateModelProviderInput } from '../../api/types';

export type ModelsScope = { kind: 'cluster' | 'account' };
export interface ModelsAdminApi {
  scope: ModelsScope;
  providersQuery: UseQueryResult<ModelProvider[]>;
  createProvider: UseMutationResult<ModelProvider, unknown, CreateModelProviderInput>;
  updateProvider: UseMutationResult<ModelProvider, unknown, { id: string; input: UpdateModelProviderInput }>;
  deleteProvider: UseMutationResult<void, unknown, string>;
  verifyProvider: UseMutationResult<ModelProviderVerification, unknown, string>;
  createModel: UseMutationResult<ProviderModel, unknown, { providerId: string; input: CreateProviderModelInput }>;
  updateClusterModel: UseMutationResult<unknown, unknown, { id: string; input: UpdateModelInput & { enabled?: boolean } }>;
  deleteModel: UseMutationResult<void, unknown, { providerId: string; id: string }>;
  useCatalog: (providerId: string, open: boolean) => UseQueryResult<CatalogModel[]>;
}

export function useModelsAdminApi(scope: ModelsScope): ModelsAdminApi {
  const api = useApi();
  const auth = useOptionalAuth();
  const qc = useQueryClient();
  const personal = scope.kind === 'account';
  const providerKey = personal ? ['account-model-providers', auth?.me?.user.id] : qk.modelProviders;
  const providersQuery = useQuery({ queryKey: providerKey, queryFn: () => personal ? api.listAccountModelProviders() : api.listModelProviders() });
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: providerKey });
    void qc.invalidateQueries({ queryKey: qk.accountModels });
    void qc.invalidateQueries({ queryKey: qk.models });
    if (!personal) void qc.invalidateQueries({ queryKey: ['model-provider-catalog'] });
    void qc.invalidateQueries({ queryKey: ['project-models'] });
  };
  const createProvider = useMutation<ModelProvider, unknown, CreateModelProviderInput>({ mutationFn: input => personal ? api.createAccountModelProvider(input) : api.createModelProvider(input), onSuccess: invalidate });
  const updateProvider = useMutation<ModelProvider, unknown, { id: string; input: UpdateModelProviderInput }>({ mutationFn: ({ id, input }) => personal ? api.updateAccountModelProvider(id, input) : api.updateModelProvider(id, input), onSuccess: invalidate });
  const deleteProvider = useMutation<void, unknown, string>({ mutationFn: id => personal ? api.deleteAccountModelProvider(id) : api.deleteModelProvider(id), onSuccess: invalidate });
  const verifyProvider = useMutation<ModelProviderVerification, unknown, string>({ mutationFn: id => personal ? api.verifyAccountModelProvider(id) : api.verifyModelProvider(id), onSettled: invalidate });
  const createModel = useMutation<ProviderModel, unknown, { providerId: string; input: CreateProviderModelInput }>({ mutationFn: ({ providerId, input }) => personal ? api.createAccountProviderModel(providerId, input) : api.createProviderModel(providerId, input), onSuccess: invalidate });
  const updateClusterModel = useMutation<unknown, unknown, { id: string; input: UpdateModelInput & { enabled?: boolean } }>({ mutationFn: ({ id, input }) => {
    if (!personal) return api.updateModel(id, input);
    const provider = providersQuery.data?.find(item => item.models.some(model => model.id === id));
    if (!provider) throw new Error('Model provider no longer available. Refresh and try again.');
    return api.updateAccountProviderModel(provider.id, id, { name: input.name, context_window: input.context_window, capabilities: input.capabilities, enabled: input.enabled });
  }, onSuccess: invalidate });
  const deleteModel = useMutation<void, unknown, { providerId: string; id: string }>({ mutationFn: ({providerId, id}) => personal ? api.deleteAccountProviderModel(providerId, id) : api.deleteModel(id), onSuccess: invalidate });
  const useCatalog = (id: string, open: boolean) => useQuery({ queryKey: personal ? [...providerKey, id, 'catalog'] : qk.modelProviderCatalog(id), queryFn: () => personal ? api.accountModelProviderCatalog(id) : api.getModelProviderCatalog(id), enabled: open && !!id, retry: false });
  return { scope, providersQuery, createProvider, updateProvider, deleteProvider, verifyProvider, createModel, updateClusterModel, deleteModel, useCatalog };
}
