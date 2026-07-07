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
import type { MemberRole, Project, Run } from '../api/types';
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

function makeClient(role?: MemberRole): { client: ApiClient; ctl: Ctl } {
  const ctl: Ctl = { streamCalls: [], getRun: vi.fn() };
  const client: Partial<ApiClient> = {
    getRun: ctl.getRun as ApiClient['getRun'],
    listEvents: async () => [],
    streamRun: (_id: string, _after: number, cb: StreamCallbacks): StreamHandle => {
      ctl.streamCalls.push({ cb });
      return { close: () => {} };
    },
    diffDownloadUrl: () => '',
    // The page reads the run's project to learn the requesting principal's role.
    ...(role
      ? {
          getProject: async () =>
            ({ id: 'proj1', name: 'demo', repo_url: '', default_branch: 'main', created_at: '', role }) as Project,
        }
      : {}),
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

  // ST-1: the draft PR chip renders only when pr_url is present, shows the mono
  // "#N", and opens the Gitea PR in a new tab.
  it('renders the draft-PR chip with the PR number and opens in a new tab', async () => {
    const prRun = baseRun({
      status: 'succeeded',
      finished_at: '2026-07-07T00:05:00Z',
      pr_url: 'https://gitea.local/jcloud/seed/pulls/42',
      pr_number: 42,
    });
    const { client, ctl } = makeClient();
    ctl.getRun.mockResolvedValue(prRun);
    renderPage(client, prRun);

    const link = (await screen.findByTestId('pr-link')) as HTMLAnchorElement;
    expect(link.textContent).toContain('Draft PR');
    expect(link.textContent).toContain('#42');
    expect(link.getAttribute('href')).toBe('https://gitea.local/jcloud/seed/pulls/42');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toContain('noreferrer');
  });

  // No PR link when the run has no pr_url (readonly / diff-only run).
  it('does not render the draft-PR chip when pr_url is absent', async () => {
    const noPr = baseRun({ status: 'succeeded', finished_at: '2026-07-07T00:05:00Z' });
    const { client, ctl } = makeClient();
    ctl.getRun.mockResolvedValue(noPr);
    renderPage(client, noPr);

    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    expect(screen.queryByTestId('pr-link')).toBeNull();
  });
});

describe('RunDetailPage — viewer gating (blueprint §2)', () => {
  it('hides Cancel on a running run for a viewer', async () => {
    const { client, ctl } = makeClient('viewer');
    ctl.getRun.mockResolvedValue(baseRun({ status: 'running' }));
    renderPage(client, baseRun({ status: 'running' }));

    await waitFor(() => expect(screen.getByTestId('run-status-header')).toBeTruthy());
    await waitFor(() => expect(screen.queryByTestId('cancel-btn')).toBeNull());
    expect(screen.queryByTestId('retry-btn')).toBeNull();
  });

  it('shows Retry on a finished run for a member', async () => {
    const { client, ctl } = makeClient('member');
    ctl.getRun.mockResolvedValue(baseRun({ status: 'failed', finished_at: '2026-07-07T00:05:00Z' }));
    renderPage(client, baseRun({ status: 'failed', finished_at: '2026-07-07T00:05:00Z' }));

    await waitFor(() => expect(screen.getByTestId('retry-btn')).toBeTruthy());
  });
});
