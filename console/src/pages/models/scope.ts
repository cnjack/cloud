/* Cluster model administration. Model authorization is Account-scoped. */
import type { UseMutationResult, UseQueryResult } from '@tanstack/react-query';
import {
  useCreateModelProvider,
  useCreateProviderModel,
  useDeleteModelProvider,
  useModelProviderCatalog,
  useModelProviders,
  useUpdateModel,
  useUpdateModelProvider,
  useVerifyModelProvider,
} from '../../api/queries';
import type {
  CatalogModel,
  CreateModelProviderInput,
  CreateProviderModelInput,
  Model,
  ModelProvider,
  ModelProviderVerification,
  ProviderModel,
  UpdateModelInput,
  UpdateModelProviderInput,
} from '../../api/types';

export type ModelsScope = { kind: 'cluster' };

export interface ModelsAdminApi {
  scope: ModelsScope;
  providersQuery: UseQueryResult<ModelProvider[]>;
  createProvider: UseMutationResult<ModelProvider, unknown, CreateModelProviderInput>;
  updateProvider: UseMutationResult<ModelProvider, unknown, { id: string; input: UpdateModelProviderInput }>;
  deleteProvider: UseMutationResult<void, unknown, string>;
  verifyProvider: UseMutationResult<ModelProviderVerification, unknown, string>;
  createModel: UseMutationResult<ProviderModel, unknown, { providerId: string; input: CreateProviderModelInput }>;
  updateClusterModel: UseMutationResult<Model, unknown, { id: string; input: UpdateModelInput }>;
  useCatalog: (providerId: string, open: boolean) => UseQueryResult<CatalogModel[]>;
}

export function useModelsAdminApi(scope: ModelsScope): ModelsAdminApi {
  const providersQuery = useModelProviders(true);
  const createProvider = useCreateModelProvider();
  const updateProvider = useUpdateModelProvider();
  const deleteProvider = useDeleteModelProvider();
  const verifyProvider = useVerifyModelProvider();
  const createModel = useCreateProviderModel();
  const updateClusterModel = useUpdateModel();
  const useCatalog = (providerId: string, open: boolean) => useModelProviderCatalog(providerId, open);

  return {
    scope,
    providersQuery,
    createProvider,
    updateProvider,
    deleteProvider,
    verifyProvider,
    createModel,
    updateClusterModel,
    useCatalog,
  };
}
