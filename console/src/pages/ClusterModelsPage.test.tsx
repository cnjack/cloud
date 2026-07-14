import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import { ApiError, type ApiClient } from '../api/client';
import type { ModelProvider, Project } from '../api/types';
import { ToastProvider } from '../components/Toast';
import { ClusterModelsPage } from './ClusterModelsPage';

const provider: ModelProvider = {
  id: 'prv-plan', name: 'Coding Plan', kind: 'openai', base_url: 'https://coding-plan.internal/v1',
  auth_type: 'service_identity', api_key_set: false, catalog_mode: 'disabled', catalog_available: false,
  project_grants: 1, created_at: '', updated_at: '', updated_by: 'admin',
  models: [{
    id: 'mdl-plan', provider_id: 'prv-plan', name: 'Coding Plan', model_id: 'coding-plan',
    runtime_model_name: 'openai/coding-plan', context_window: 32_000,
    capabilities: { reasoning: false, tools: true, image: false }, source: 'custom',
    granted_project_ids: ['p1'],
  }],
};

const project: Project = { id: 'p1', name: 'jcode Cloud', created_at: '', services: [] };

function renderPage(overrides: Partial<ApiClient> = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = {
    listModelProviders: async () => [provider], listProjects: async () => [project],
    getModelProviderCatalog: vi.fn().mockRejectedValue(new ApiError(409, 'catalog unavailable', { error: { code: 'catalog_unavailable' } })),
    createProviderModel: vi.fn().mockResolvedValue(provider.models[0]),
    grantModel: vi.fn().mockResolvedValue({}), revokeModel: vi.fn().mockResolvedValue({}),
    verifyModelProvider: vi.fn().mockResolvedValue({ reachable: true, catalog_available: false, latency_ms: 42 }),
    ...overrides,
  } as unknown as ApiClient;
  render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client} role="cluster-admin">
        <ToastProvider><MemoryRouter><ClusterModelsPage /></MemoryRouter></ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
  return client;
}

describe('ClusterModelsPage', () => {
  it('keeps Custom model and Project grant controls aligned in their own columns', async () => {
    renderPage();
    const card = await screen.findByTestId('provider-card-prv-plan');
    expect(within(card).getByRole('button', { name: 'Custom model' })).toBeTruthy();
    const grant = within(card).getByTestId('grant-count-mdl-plan');
    expect(grant.textContent).toContain('1');
    expect(grant.textContent).toContain('Project grant');
    expect(within(card).getByRole('button', { name: 'Manage grants' })).toBeTruthy();
  });

  it('surfaces a disabled catalog and creates a custom model explicitly', async () => {
    const client = renderPage();
    await screen.findByTestId('provider-card-prv-plan');
    expect((screen.getByRole('button', { name: 'Catalog unavailable' }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/does not expose a model catalog/i)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Custom model' }));
    const dialog = await screen.findByRole('dialog', { name: 'Custom model · Coding Plan' });
    fireEvent.change(within(dialog).getByLabelText(/Display name/), { target: { value: 'Plan Mini' } });
    fireEvent.change(within(dialog).getByLabelText(/Model ID/), { target: { value: 'plan-mini' } });
    fireEvent.change(within(dialog).getByLabelText('Context window'), { target: { value: '64000' } });
    fireEvent.click(within(dialog).getByLabelText('Tool use'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add custom model' }));

    await waitFor(() => expect(client.createProviderModel).toHaveBeenCalledWith('prv-plan', {
      name: 'Plan Mini', model_id: 'plan-mini', context_window: 64000,
      capabilities: { reasoning: false, tools: true, image: false }, source: 'custom',
    }));
  });

  it('manages Project grants through the existing grant contract', async () => {
    const client = renderPage();
    await screen.findByTestId('provider-card-prv-plan');
    fireEvent.click(screen.getByRole('button', { name: 'Manage grants' }));
    const checkbox = screen.getByRole('checkbox', { name: 'jcode Cloud' });
    expect((checkbox as HTMLInputElement).checked).toBe(true);
    fireEvent.click(checkbox);
    await waitFor(() => expect(client.revokeModel).toHaveBeenCalledWith('mdl-plan', 'p1'));
  });

  it('refreshes the provider card after a failed verification probe', async () => {
    let reads = 0;
    const listModelProviders = vi.fn().mockImplementation(async () => {
      reads += 1;
      return [{
        ...provider,
        last_verification_error: reads > 1 ? 'provider connection refused' : '',
      }];
    });
    const verifyModelProvider = vi.fn().mockRejectedValue(
      new ApiError(502, 'could not reach the model provider'),
    );
    renderPage({ listModelProviders, verifyModelProvider });
    const card = await screen.findByTestId('provider-card-prv-plan');

    fireEvent.click(within(card).getByRole('button', { name: 'Test' }));

    await waitFor(() => expect(verifyModelProvider).toHaveBeenCalledWith('prv-plan'));
    expect(await within(card).findByText('Last test: provider connection refused')).toBeTruthy();
    expect(listModelProviders.mock.calls.length).toBeGreaterThanOrEqual(2);
  });
});
