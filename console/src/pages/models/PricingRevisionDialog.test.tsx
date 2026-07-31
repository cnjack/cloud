import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../../api/ApiProvider';
import type { ApiClient } from '../../api/client';
import type { ProviderModel } from '../../api/types';
import { PricingRevisionDialog } from './ModelsCatalog';

const model: ProviderModel = {
  id: 'model-1',
  provider_id: 'provider-1',
  name: 'Reasoner',
  model_id: 'reasoner-v1',
  runtime_model_name: 'provider/reasoner-v1',
  context_window: 128000,
  capabilities: { reasoning: true, tools: true, image: false },
  source: 'catalog',
};

describe('PricingRevisionDialog', () => {
  it('shows immutable history and converts currency-unit rates to micros', async () => {
    const create = vi.fn(async (_modelId: string, input: Parameters<ApiClient['createModelPricingRevision']>[1]) => ({
      id: 'price-new',
      model_id: 'model-1',
      model_name: 'Reasoner',
      ...input,
      created_at: '2026-07-31T00:00:00Z',
    }));
    const client = {
      listModelPricingRevisions: async () => [{
        id: 'price-old',
        model_id: 'model-1',
        model_name: 'Reasoner',
        currency: 'USD',
        input_micros_per_million: 1_000_000,
        output_micros_per_million: 5_000_000,
        cache_read_micros_per_million: null,
        cache_write_micros_per_million: null,
        effective_at: '2026-07-01T00:00:00Z',
        created_at: '2026-07-01T00:00:00Z',
      }],
      createModelPricingRevision: create,
    } as unknown as ApiClient;
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ApiProvider client={client}>
          <PricingRevisionDialog model={model} open onClose={() => {}} />
        </ApiProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByText(/Pricing revisions are immutable/)).toBeTruthy();
    expect(await screen.findByText(/Input 1 · Output 5/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Output / 1M tokens'), { target: { value: '7.25' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add revision' }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0]?.[1].output_micros_per_million).toBe(7_250_000);
    expect(create.mock.calls[0]?.[1].input_micros_per_million).toBeNull();
    expect(Math.abs(Date.parse(create.mock.calls[0]?.[1].effective_at ?? '') - Date.now()))
      .toBeLessThan(2 * 60_000);
  });
});
