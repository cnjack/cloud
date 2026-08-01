/*
 * ProjectModelsPanel (M2) — owner manages the project's own providers/models
 * (add provider, add custom model, per-model enable toggle, delete); a member
 * sees only the read-only "available models" union. Mutations invalidate the
 * project-models union so the available list stays honest.
 */
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../../api/ApiProvider';
import { ApiError, type ApiClient } from '../../api/client';
import type { ModelProvider, Project, ProviderModel } from '../../api/types';
import { ToastProvider } from '../../components/Toast';
import { ProjectModelsPanel } from './ProjectModelsPanel';

const providerModel: ProviderModel = {
  id: 'mdl-1', provider_id: 'prv-1', name: 'Plan', model_id: 'plan',
  runtime_model_name: 'openai/plan', context_window: 32_000,
  capabilities: { reasoning: false, tools: true, image: false }, source: 'custom', enabled: true,
};

const seedProvider: ModelProvider = {
  id: 'prv-1', project_id: 'p1', name: 'Project OpenAI', kind: 'openai',
  base_url: 'https://api.example.com/v1', auth_type: 'api_key', api_key_set: true,
  headers_set: false, catalog_mode: 'disabled', catalog_available: false,
  models: [providerModel], created_at: '', updated_at: '', updated_by: 'admin',
};

interface Ctl {
  createdProviders: unknown[];
  createdModels: { providerId: string; input: unknown }[];
  updated: { providerId: string; modelId: string; input: { enabled?: boolean } }[];
  deleted: { providerId: string; modelId: string }[];
  projectUpdates: unknown[];
}

function clone(p: ModelProvider): ModelProvider {
  return { ...p, models: p.models.map((m) => ({ ...m, capabilities: { ...m.capabilities } })) };
}

function makeClient(seed: ModelProvider[] = [seedProvider]): { client: ApiClient; ctl: Ctl } {
  const providers = seed.map(clone);
  const ctl: Ctl = { createdProviders: [], createdModels: [], updated: [], deleted: [], projectUpdates: [] };
  const client: Partial<ApiClient> = {
    listProjectModelProviders: async () => providers.map(clone),
    listProjectModels: async () => ({
      models: providers.flatMap((p) => p.models).filter((m) => m.enabled !== false)
        .map((m) => ({
          id: m.id,
          name: m.name,
          model_name: m.runtime_model_name,
          capabilities: m.capabilities,
        })),
      env_fallback: false,
    }),
    updateProject: async (_id, input) => {
      ctl.projectUpdates.push(input);
      return { id: 'p1', name: 'Project', created_at: '', role: 'owner', ...input } as Project;
    },
    createProjectModelProvider: async (_pid, input) => {
      ctl.createdProviders.push(input);
      const np: ModelProvider = { ...seedProvider, id: 'prv-new', name: input.name, models: [] };
      providers.push(np);
      return clone(np);
    },
    verifyProjectModelProvider: async () => ({ reachable: true, catalog_available: false, latency_ms: 5 }),
    getProjectModelProviderCatalog: vi.fn().mockRejectedValue(
      new ApiError(409, 'catalog unavailable', { error: { code: 'catalog_unavailable' } }),
    ),
    createProjectProviderModel: async (_pid, providerId, input) => {
      ctl.createdModels.push({ providerId, input });
      const m: ProviderModel = {
        ...providerModel, id: 'mdl-new', name: input.name, model_id: input.model_id,
        runtime_model_name: `openai/${input.model_id}`, capabilities: { ...input.capabilities },
        source: input.source, enabled: true,
      };
      providers.find((p) => p.id === providerId)!.models.push(m);
      return { ...m };
    },
    updateProjectProviderModel: async (_pid, providerId, modelId, input) => {
      ctl.updated.push({ providerId, modelId, input });
      const m = providers.find((p) => p.id === providerId)!.models.find((x) => x.id === modelId)!;
      Object.assign(m, input);
      return { ...m };
    },
    deleteProjectProviderModel: async (_pid, providerId, modelId) => {
      ctl.deleted.push({ providerId, modelId });
      const p = providers.find((pp) => pp.id === providerId)!;
      p.models = p.models.filter((x) => x.id !== modelId);
    },
  };
  return { client: client as ApiClient, ctl };
}

function renderPanel(canManage: boolean, client: ApiClient) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <ProjectModelsPanel
            projectId="p1"
            canManage={canManage}
            project={{ id: 'p1', name: 'Project', created_at: '', role: canManage ? 'owner' : 'member' }}
          />
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

afterEach(() => vi.restoreAllMocks());

describe('ProjectModelsPanel', () => {
  it('owner sees the provider card and the available-models union', async () => {
    const { client } = makeClient();
    renderPanel(true, client);

    expect(await screen.findByTestId('provider-card-prv-1')).toBeTruthy();
    expect(screen.getByTestId('project-add-provider')).toBeTruthy();
    // The enabled model shows in the read-only available list.
    const available = screen.getByTestId('project-available-models');
    await waitFor(() => expect(within(available).getByText('openai/plan')).toBeTruthy());
  });

  it('owner can set the project default model', async () => {
    const { client, ctl } = makeClient();
    renderPanel(true, client);

    fireEvent.click(await screen.findByTestId('project-default-model-select'));
    fireEvent.click(await screen.findByRole('option', { name: 'Plan' }));

    await waitFor(() => expect(ctl.projectUpdates).toEqual([{ default_model_id: 'mdl-1' }]));
  });

  it('owner adds a project provider', async () => {
    const { client, ctl } = makeClient();
    renderPanel(true, client);
    await screen.findByTestId('provider-card-prv-1');

    fireEvent.click(screen.getByTestId('project-add-provider'));
    const dialog = await screen.findByRole('dialog', { name: 'Add model provider' });
    fireEvent.click(within(dialog).getByTestId('provider-mode-custom'));
    fireEvent.change(within(dialog).getByLabelText(/Provider name/), { target: { value: 'My provider' } });
    fireEvent.change(within(dialog).getByLabelText(/Provider type/), { target: { value: 'openai' } });
    fireEvent.click(within(dialog).getByTestId('provider-advanced-toggle'));
    fireEvent.change(within(dialog).getByLabelText(/Base URL/), { target: { value: 'https://api.acme.com/v1' } });
    fireEvent.change(within(dialog).getByLabelText(/API key/), { target: { value: 'sk-test' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add provider' }));

    await waitFor(() => expect(ctl.createdProviders).toHaveLength(1));
    expect(ctl.createdProviders[0]).toMatchObject({
      name: 'My provider', kind: 'openai', base_url: 'https://api.acme.com/v1',
      auth_type: 'api_key', api_key: 'sk-test', catalog_mode: 'auto',
    });
  });

  it('owner selects the same named provider preset used by Desktop', async () => {
    const { client, ctl } = makeClient();
    renderPanel(true, client);
    await screen.findByTestId('provider-card-prv-1');

    fireEvent.click(screen.getByTestId('project-add-provider'));
    const dialog = await screen.findByRole('dialog', { name: 'Add model provider' });
    fireEvent.change(within(dialog).getByLabelText('Provider'), {
      target: { value: 'zhipuai-coding-plan' },
    });
    fireEvent.change(within(dialog).getByLabelText(/API key/), { target: { value: 'sk-plan' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add provider' }));

    await waitFor(() => expect(ctl.createdProviders).toHaveLength(1));
    expect(ctl.createdProviders[0]).toMatchObject({
      name: 'Zhipu AI Coding Plan',
      kind: 'zhipuai-coding-plan',
      base_url: 'https://open.bigmodel.cn/api/coding/paas/v4',
    });
  });

  it('owner attaches custom request headers through the Advanced disclosure', async () => {
    const { client, ctl } = makeClient();
    renderPanel(true, client);
    await screen.findByTestId('provider-card-prv-1');

    fireEvent.click(screen.getByTestId('project-add-provider'));
    const dialog = await screen.findByRole('dialog', { name: 'Add model provider' });
    fireEvent.click(within(dialog).getByTestId('provider-mode-custom'));
    fireEvent.change(within(dialog).getByLabelText(/Provider name/), { target: { value: 'Hdr' } });
    fireEvent.change(within(dialog).getByLabelText(/Provider type/), { target: { value: 'openai' } });
    fireEvent.click(within(dialog).getByTestId('provider-advanced-toggle'));
    fireEvent.change(within(dialog).getByLabelText(/Base URL/), { target: { value: 'https://api.acme.com/v1' } });
    fireEvent.change(within(dialog).getByLabelText(/API key/), { target: { value: 'sk' } });
    fireEvent.click(within(dialog).getByTestId('add-header'));
    fireEvent.change(within(dialog).getByTestId('header-key-0'), { target: { value: 'X-Org' } });
    fireEvent.change(within(dialog).getByTestId('header-value-0'), { target: { value: 'acme' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add provider' }));

    await waitFor(() => expect(ctl.createdProviders).toHaveLength(1));
    expect(ctl.createdProviders[0]).toMatchObject({ headers: { 'X-Org': 'acme' } });
  });

  it('owner adds a custom model to a provider', async () => {
    const { client, ctl } = makeClient();
    renderPanel(true, client);
    const card = await screen.findByTestId('provider-card-prv-1');

    fireEvent.click(within(card).getByRole('button', { name: 'Custom model' }));
    const dialog = await screen.findByRole('dialog', { name: 'Custom model · Project OpenAI' });
    fireEvent.change(within(dialog).getByLabelText(/Display name/), { target: { value: 'Mini' } });
    fireEvent.change(within(dialog).getByLabelText(/Model ID/), { target: { value: 'mini' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add custom model' }));

    await waitFor(() => expect(ctl.createdModels).toHaveLength(1));
    expect(ctl.createdModels[0]).toMatchObject({
      providerId: 'prv-1',
      input: { name: 'Mini', model_id: 'mini', source: 'custom' },
    });
  });

  it('owner syncs an existing catalog model from models.dev metadata', async () => {
    const catalogProvider: ModelProvider = {
      ...seedProvider,
      catalog_mode: 'auto',
      catalog_available: true,
      models: [{
        ...providerModel,
        source: 'catalog',
        context_window: 0,
        capabilities: { reasoning: false, tools: false, image: false },
      }],
    };
    const { client, ctl } = makeClient([catalogProvider]);
    client.getProjectModelProviderCatalog = vi.fn().mockResolvedValue([{
      id: 'plan',
      name: 'Plan',
      context_window: 1_000_000,
      capabilities: { reasoning: true, tools: true, image: false },
      metadata_source: 'models.dev',
    }]);
    renderPanel(true, client);

    const card = await screen.findByTestId('provider-card-prv-1');
    fireEvent.click(within(card).getByRole('button', { name: 'Browse catalog' }));
    const dialog = await screen.findByRole('dialog', { name: 'Catalog · Project OpenAI' });
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Sync metadata' }));

    await waitFor(() => expect(ctl.updated).toEqual([{
      providerId: 'prv-1',
      modelId: 'mdl-1',
      input: {
        name: 'Plan',
        context_window: 1_000_000,
        capabilities: { reasoning: true, tools: true, image: false },
      },
    }]));
  });

  it('owner toggles a model off, which PATCHes enabled=false and drops it from the union', async () => {
    const { client, ctl } = makeClient();
    renderPanel(true, client);
    await screen.findByTestId('provider-card-prv-1');

    const available = screen.getByTestId('project-available-models');
    await waitFor(() => expect(within(available).getByText('openai/plan')).toBeTruthy());

    // The enabled model's switch is labelled "Disable …".
    fireEvent.click(screen.getByRole('switch', { name: 'Disable Plan' }));

    await waitFor(() => expect(ctl.updated).toHaveLength(1));
    expect(ctl.updated[0]).toEqual({ providerId: 'prv-1', modelId: 'mdl-1', input: { enabled: false } });
    // Invalidation refreshed the union: the disabled model leaves the available list.
    await waitFor(() =>
      expect(within(screen.getByTestId('project-available-models')).queryByText('openai/plan')).toBeNull(),
    );
  });

  it('owner deletes a model behind a confirm', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { client, ctl } = makeClient();
    renderPanel(true, client);
    const card = await screen.findByTestId('provider-card-prv-1');

    fireEvent.click(within(card).getByRole('button', { name: 'Remove Plan' }));

    await waitFor(() => expect(ctl.deleted).toEqual([{ providerId: 'prv-1', modelId: 'mdl-1' }]));
  });

  it('a member gets the read-only union with no management controls', async () => {
    const { client } = makeClient();
    renderPanel(false, client);

    // The available list still renders…
    const available = await screen.findByTestId('project-available-models');
    expect(within(available).getByText('openai/plan')).toBeTruthy();
    // …but no provider cards and no add/edit/toggle affordances.
    expect(screen.queryByTestId('project-add-provider')).toBeNull();
    expect(screen.queryByTestId('provider-card-prv-1')).toBeNull();
    expect(screen.queryByRole('switch')).toBeNull();
  });
});
