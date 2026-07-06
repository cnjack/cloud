/*
 * RunDetailPage.test.tsx — page-level error-state coverage:
 *   - Finding #4: a failed *refetch* while run.data is cached must NOT swap the
 *     whole page to the ErrorBlock dead-end; the cached run stays rendered with
 *     a non-blocking notice.
 *   - Finding #3: a fatal stream error while the run is non-terminal surfaces a
 *     stream-error banner with a Reconnect action.
 */
import { describe, expect, it, vi } from 'vitest';

import { render, screen, waitFor, act } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import { ApiError, type ApiClient, type StreamCallbacks, type StreamHandle } from '../api/client';
import type { Run } from '../api/types';
import { qk } from '../api/queries';
import { RunDetailPage } from './RunDetailPage';

function baseRun(overrides: Partial<Run> = {}): Run {
  return {
    id: 'run1',
    project_id: 'proj1',
    prompt: 'Add a line Hello to README',
    status: 'running',
    attempt: 1,
    created_at: '2026-07-07T00:00:00Z',
    started_at: '2026-07-07T00:00:01Z',
    finished_at: null,
    ...overrides,
  };
}

interface Ctl {
  streamCalls: { cb: StreamCallbacks }[];
  getRun: ReturnType<typeof vi.fn>;
}

function makeClient(): { client: ApiClient; ctl: Ctl } {
  const ctl: Ctl = { streamCalls: [], getRun: vi.fn() };
  const client: Partial<ApiClient> = {
    getRun: ctl.getRun as ApiClient['getRun'],
    listEvents: async () => [],
    streamRun: (_id: string, _after: number, cb: StreamCallbacks): StreamHandle => {
      ctl.streamCalls.push({ cb });
      return { close: () => {} };
    },
    diffDownloadUrl: () => '',
  };
  return { client: client as ApiClient, ctl };
}

function renderPage(client: ApiClient, seed?: Run) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  if (seed) qc.setQueryData(qk.run(seed.id), seed);
  const ui = (
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <MemoryRouter initialEntries={['/runs/run1']}>
            <Routes>
              <Route path="/runs/:runId" element={<RunDetailPage />} />
            </Routes>
          </MemoryRouter>
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>
  );
  return { qc, ...render(ui) };
}

describe('RunDetailPage — resilient error states', () => {
  it('keeps the cached run rendered when a refetch fails (no whole-page dead-end)', async () => {
    const { client, ctl } = makeClient();
    // First read (initial mount) succeeds; a later refetch rejects.
    ctl.getRun
      .mockResolvedValueOnce(baseRun())
      .mockRejectedValue(new ApiError(500, 'orchestrator hiccup'));

    const { qc } = renderPage(client, baseRun());

    // Page shows the run header (not the ErrorBlock).
    await waitFor(() =>
      expect(screen.getByTestId('run-status-header')).toBeTruthy(),
    );

    // Force a failing refetch (as useRunStream does on terminal status).
    await act(async () => {
      await qc.refetchQueries({ queryKey: qk.run('run1') });
    });

    // The query is in error, but data is still cached → page stays, no dead-end.
    expect(screen.queryByText("Couldn't load run")).toBeNull();
    expect(screen.getByTestId('run-status-header')).toBeTruthy();
  });

  it('surfaces a stream-error banner with a Reconnect action on fatal SSE error', async () => {
    const { client, ctl } = makeClient();
    ctl.getRun.mockResolvedValue(baseRun());
    renderPage(client, baseRun());

    await waitFor(() => expect(ctl.streamCalls.length).toBe(1));

    act(() => ctl.streamCalls[0]!.cb.onError?.(new ApiError(401, 'unauthorized')));

    await waitFor(() => expect(screen.getByTestId('stream-error')).toBeTruthy());
    expect(screen.getByTestId('stream-reconnect')).toBeTruthy();
  });
});
