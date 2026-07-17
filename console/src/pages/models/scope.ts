/*
 * scope.ts — the ModelsScope discriminator and the useModelsAdminApi adapter.
 *
 * The cluster catalog (ClusterModelsPage) and the project-scoped manager
 * (ProjectModelsPanel) share ONE set of components. This adapter maps a scope to
 * a uniform admin surface so those components never branch on which set of hooks
 * to call: both hook families are invoked unconditionally (React rules of hooks)
 * with the inactive one gated `enabled: false`, then the active one is returned.
 */
import {
  useCreateModelProvider,
  useCreateProjectModelProvider,
  useCreateProjectProviderModel,
  useCreateProviderModel,
  useDeleteModelProvider,
  useDeleteProjectModelProvider,
  useDeleteProjectProviderModel,
  useModelProviderCatalog,
  useModelProviders,
  useProjectModelProviderCatalog,
  useProjectModelProviders,
  useUpdateProjectModelProvider,
  useUpdateProjectProviderModel,
  useUpdateModelProvider,
  useVerifyModelProvider,
  useVerifyProjectModelProvider,
} from '../../api/queries';
import type {
  CatalogModel,
  CreateModelProviderInput,
  CreateProviderModelInput,
  ModelProvider,
  ModelProviderVerification,
  ProviderModel,
  UpdateModelProviderInput,
  UpdateProviderModelInput,
} from '../../api/types';
import type { UseMutationResult, UseQueryResult } from '@tanstack/react-query';

export type ModelsScope =
  | { kind: 'cluster' }
  | { kind: 'project'; projectId: string };

type ProvidersQuery = UseQueryResult<ModelProvider[]>;
type CatalogQuery = UseQueryResult<CatalogModel[]>;

/**
 * The uniform admin surface the shared catalog components consume. `updateModel`
 * / `deleteModel` are project-only (cluster models are managed by grants), so
 * they are optional; a component renders those controls only in project scope.
 */
export interface ModelsAdminApi {
  scope: ModelsScope;
  providersQuery: ProvidersQuery;
  createProvider: UseMutationResult<ModelProvider, unknown, CreateModelProviderInput>;
  updateProvider: UseMutationResult<ModelProvider, unknown, { id: string; input: UpdateModelProviderInput }>;
  deleteProvider: UseMutationResult<void, unknown, string>;
  verifyProvider: UseMutationResult<ModelProviderVerification, unknown, string>;
  createModel: UseMutationResult<ProviderModel, unknown, { providerId: string; input: CreateProviderModelInput }>;
  updateModel?: UseMutationResult<ProviderModel, unknown, { providerId: string; modelId: string; input: UpdateProviderModelInput }>;
  deleteModel?: UseMutationResult<void, unknown, { providerId: string; modelId: string }>;
  /** A stable hook (safe to call at a component's top level) for a provider's catalog. */
  useCatalog: (providerId: string, open: boolean) => CatalogQuery;
}

export function useModelsAdminApi(scope: ModelsScope): ModelsAdminApi {
  const isCluster = scope.kind === 'cluster';
  const projectId = scope.kind === 'project' ? scope.projectId : '';

  // Both families run every render; the inactive family is gated off.
  const clusterProviders = useModelProviders(isCluster);
  const projectProviders = useProjectModelProviders(projectId, !isCluster);

  const createClusterProvider = useCreateModelProvider();
  const updateClusterProvider = useUpdateModelProvider();
  const deleteClusterProvider = useDeleteModelProvider();
  const verifyClusterProvider = useVerifyModelProvider();
  const createClusterModel = useCreateProviderModel();

  const createProjectProvider = useCreateProjectModelProvider(projectId);
  const updateProjectProvider = useUpdateProjectModelProvider(projectId);
  const deleteProjectProvider = useDeleteProjectModelProvider(projectId);
  const verifyProjectProvider = useVerifyProjectModelProvider(projectId);
  const createProjectModel = useCreateProjectProviderModel(projectId);
  const updateProjectModel = useUpdateProjectProviderModel(projectId);
  const deleteProjectModel = useDeleteProjectProviderModel(projectId);

  const useCatalog = (providerId: string, open: boolean): CatalogQuery => {
    const clusterCatalog = useModelProviderCatalog(providerId, isCluster && open);
    const projectCatalog = useProjectModelProviderCatalog(projectId, providerId, !isCluster && open);
    return isCluster ? clusterCatalog : projectCatalog;
  };

  if (isCluster) {
    return {
      scope,
      providersQuery: clusterProviders,
      createProvider: createClusterProvider,
      updateProvider: updateClusterProvider,
      deleteProvider: deleteClusterProvider,
      verifyProvider: verifyClusterProvider,
      createModel: createClusterModel,
      useCatalog,
    };
  }
  return {
    scope,
    providersQuery: projectProviders,
    createProvider: createProjectProvider,
    updateProvider: updateProjectProvider,
    deleteProvider: deleteProjectProvider,
    verifyProvider: verifyProjectProvider,
    createModel: createProjectModel,
    updateModel: updateProjectModel,
    deleteModel: deleteProjectModel,
    useCatalog,
  };
}
