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
    granted_account_ids: ['u1'],
  }],
};

const project: Project = { id: 'p1', name: 'jcode Cloud', created_at: '', services: [] };
const account = { id: 'u1', display_name: 'Ada Lovelace', is_cluster_admin: false };

function renderPage(overrides: Partial<ApiClient> = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = {
    listModelProviders: async () => [provider], listProjects: async () => [project],
    searchUsers: async () => [account],
    getModelProviderCatalog: vi.fn().mockRejectedValue(new ApiError(409, 'catalog unavailable', { error: { code: 'catalog_unavailable' } })),
    createProviderModel: vi.fn().mockResolvedValue(provider.models[0]),
    grantModel: vi.fn().mockResolvedValue({}), revokeModel: vi.fn().mockResolvedValue({}),
    grantModelToAccount: vi.fn().mockResolvedValue({}), revokeModelFromAccount: vi.fn().mockResolvedValue({}),
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
  it('keeps Custom model and Account/Project access controls aligned in their own columns', async () => {
    renderPage();
    const card = await screen.findByTestId('provider-card-prv-plan');
    expect(within(card).getByRole('button', { name: 'Custom model' })).toBeTruthy();
    const grant = within(card).getByTestId('grant-count-mdl-plan');
    expect(within(grant).getByText('Account grant')).toBeTruthy();
    expect(within(grant).getByText('Project grant')).toBeTruthy();
    expect(within(card).getByRole('button', { name: 'Manage access' })).toBeTruthy();
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
    fireEvent.click(screen.getByRole('button', { name: 'Manage access' }));
    fireEvent.click(await screen.findByRole('tab', { name: 'Projects' }));
    const checkbox = await screen.findByRole('checkbox', { name: 'jcode Cloud' });
    expect((checkbox as HTMLInputElement).checked).toBe(true);
    fireEvent.click(checkbox);
    await waitFor(() => expect(client.revokeModel).toHaveBeenCalledWith('mdl-plan', 'p1'));
  });

  it('manages Account grants independently from Project grants', async () => {
    const client = renderPage();
    await screen.findByTestId('provider-card-prv-plan');
    fireEvent.click(screen.getByRole('button', { name: 'Manage access' }));
    const dialog = screen.getByRole('dialog', { name: 'Model access · Coding Plan' });
    fireEvent.click(within(dialog).getByRole('tab', { name: 'Accounts' }));
    const checkbox = await within(dialog).findByRole('checkbox', { name: 'Ada Lovelace' });
    expect((checkbox as HTMLInputElement).checked).toBe(true);
    fireEvent.click(checkbox);
    await waitFor(() => expect(client.revokeModelFromAccount).toHaveBeenCalledWith('mdl-plan', 'u1'));
    expect(client.revokeModel).not.toHaveBeenCalled();
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

  it('uses the Desktop provider preset picker at Cluster scope too', async () => {
    const createModelProvider = vi.fn().mockImplementation(async (input) => ({
      ...provider,
      id: 'prv-zhipu-plan',
      name: input.name,
      kind: input.kind,
      base_url: input.base_url,
      models: [],
    }));
    renderPage({ createModelProvider });
    fireEvent.click(await screen.findByRole('button', { name: 'Add provider' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add model provider' });
    fireEvent.change(within(dialog).getByLabelText('Provider'), {
      target: { value: 'zhipuai-coding-plan' },
    });
    fireEvent.change(within(dialog).getByLabelText(/API key/), { target: { value: 'sk-plan' } });
    fireEvent.click(within(dialog).getByTestId('provider-advanced-toggle'));
    fireEvent.click(within(dialog).getByTestId('add-header'));
    fireEvent.change(within(dialog).getByTestId('header-key-0'), { target: { value: 'X-Org' } });
    fireEvent.change(within(dialog).getByTestId('header-value-0'), { target: { value: 'cloud' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add provider' }));

    await waitFor(() => expect(createModelProvider).toHaveBeenCalledWith(expect.objectContaining({
      name: 'Zhipu AI Coding Plan',
      kind: 'zhipuai-coding-plan',
      base_url: 'https://open.bigmodel.cn/api/coding/paas/v4',
      headers: { 'X-Org': 'cloud' },
    })));
  });
});
