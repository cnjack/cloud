/*
 * PrPanel.test.tsx — the Run page PR tab (blueprint §5):
 *   - renders the PR link, live state badge and review runs (markdown)
 *   - "Request AI review" dispatches and navigates to the new review run
 *   - the button is hidden for a viewer (backend also 403s)
 *   - PrStateBadge maps every state to a label
 */
import { describe, expect, it } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ApiError } from '../api/client';
import { ToastProvider } from './Toast';
import type { ApiClient } from '../api/client';
import type { PrInfo, Run } from '../api/types';
import { PrPanel, PrStateBadge } from './PrPanel';

function basePR(over: Partial<PrInfo> = {}): PrInfo {
  return {
    url: 'https://gitea.local/jcloud/seed/pulls/7',
    state: 'open',
    head_branch: 'jcode/run-abc',
    review_runs: [
      {
        id: 'rev1',
        status: 'succeeded',
        review_output: '## Review\n\nLooks **good** overall.',
        review_posted_at: '2026-07-07T00:10:00Z',
        created_at: '2026-07-07T00:00:00Z',
        triggered_by_display_name: 'Grace Hopper',
      },
    ],
    ...over,
  };
}

function makeClient(over: Partial<ApiClient> = {}): ApiClient {
  const pr = basePR();
  return {
    getPR: async () => pr,
    // Feature A: the review button keys enable/disable off this. Default configured.
    getModelConfig: async () => ({ configured: true, source: 'env' }),
    requestReview: async () =>
      ({
        id: 'rev_new',
        project_id: 'proj1',
        kind: 'review',
        prompt: 'AI review of PR',
        status: 'queued',
        attempt: 1,
        created_at: '2026-07-07T01:00:00Z',
      }) as Run,
    ...over,
  } as ApiClient;
}

function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="loc">{loc.pathname}</div>;
}

function renderPanel(client: ApiClient, canReview = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <MemoryRouter initialEntries={['/']}>
            <LocationProbe />
            <Routes>
              <Route path="/" element={<PrPanel runId="run1" canReview={canReview} />} />
              <Route path="/runs/:id" element={<div>review run page</div>} />
            </Routes>
          </MemoryRouter>
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('PrPanel', () => {
  it('renders the PR link, an Open state badge and a review with markdown', async () => {
    renderPanel(makeClient());

    const link = (await screen.findByTestId('pr-external-link')) as HTMLAnchorElement;
    expect(link.getAttribute('href')).toBe('https://gitea.local/jcloud/seed/pulls/7');
    expect(link.getAttribute('target')).toBe('_blank');

    // State badge.
    expect(screen.getByText('Open')).toBeTruthy();
    // Head branch chip.
    expect(screen.getByText('jcode/run-abc')).toBeTruthy();

    // Review item with rendered markdown (heading + bold).
    const item = screen.getByTestId('review-item');
    expect(item.textContent).toContain('Review');
    expect(item.querySelector('strong')?.textContent).toBe('good');
    expect(item.textContent).toContain('Grace Hopper');
  });

  it('requests a review and navigates to the new review run', async () => {
    renderPanel(makeClient());

    const btn = await screen.findByTestId('request-review-btn');
    fireEvent.click(btn);

    await waitFor(() =>
      expect(screen.getByTestId('loc').textContent).toBe('/runs/rev_new'),
    );
    expect(screen.getByText('review run page')).toBeTruthy();
  });

  it('surfaces a 409 model_not_configured message in a toast (Feature A)', async () => {
    const msg =
      'the LLM is not configured — a cluster admin must set it on the Cluster page before runs can start';
    renderPanel(
      makeClient({
        requestReview: async () => {
          throw new ApiError(409, msg, { error: { code: 'model_not_configured', message: msg } });
        },
      }),
    );

    fireEvent.click(await screen.findByTestId('request-review-btn'));
    // The human-readable backend message reaches the user via the existing toast.
    await waitFor(() => expect(screen.getByText(msg)).toBeTruthy());
    // No navigation occurred (still on the panel route).
    expect(screen.getByTestId('loc').textContent).toBe('/');
  });

  it('disables Request AI review with a notice when no LLM is configured (Feature A)', async () => {
    renderPanel(
      makeClient({
        getModelConfig: async () => ({ configured: false, source: 'none' }),
      }),
    );

    const btn = (await screen.findByTestId('request-review-btn')) as HTMLButtonElement;
    await waitFor(() => expect(btn.disabled).toBe(true));
    expect(screen.getByTestId('model-not-configured')).toBeTruthy();
  });

  it('hides the Request AI review button for a viewer', async () => {
    renderPanel(makeClient(), false);
    await screen.findByTestId('pr-panel');
    expect(screen.queryByTestId('request-review-btn')).toBeNull();
  });

  it('shows an empty state when there are no reviews', async () => {
    renderPanel(makeClient({ getPR: async () => basePR({ review_runs: [] }) }));
    expect(await screen.findByTestId('reviews-empty')).toBeTruthy();
  });
});

describe('PrStateBadge', () => {
  it('labels open / merged / closed and falls back to Unknown', () => {
    const { rerender } = render(<PrStateBadge state="open" />);
    expect(screen.getByText('Open')).toBeTruthy();
    rerender(<PrStateBadge state="merged" />);
    expect(screen.getByText('Merged')).toBeTruthy();
    rerender(<PrStateBadge state="closed" />);
    expect(screen.getByText('Closed')).toBeTruthy();
    rerender(<PrStateBadge state="weird-value" />);
    expect(screen.getByText('Unknown')).toBeTruthy();
  });
});
