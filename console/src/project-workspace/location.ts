import type { Service } from '../api/types';

export const WORKSPACE_TABS = ['tasks', 'automations', 'settings'] as const;
export type WorkspaceTab = (typeof WORKSPACE_TABS)[number];

function defaultServiceId(services: readonly Service[]): string {
  return services.find((service) => service.name === 'default')?.id ?? services[0]?.id ?? '';
}

export function resolveWorkspaceLocation(
  services: readonly Service[],
  search: URLSearchParams,
  canManage: boolean,
): { serviceId: string; tab: WorkspaceTab; needsNormalization: boolean } {
  if (services.length === 0) {
    return { serviceId: '', tab: 'tasks', needsNormalization: false };
  }

  const requestedServiceId = search.get('service') ?? '';
  const serviceId = services.some((service) => service.id === requestedServiceId)
    ? requestedServiceId
    : defaultServiceId(services);

  const requestedTab = search.get('tab');
  const tab: WorkspaceTab =
    requestedTab === 'automations' || (requestedTab === 'settings' && canManage)
      ? requestedTab
      : 'tasks';

  return {
    serviceId,
    tab,
    needsNormalization: requestedServiceId !== serviceId || requestedTab !== tab,
  };
}
