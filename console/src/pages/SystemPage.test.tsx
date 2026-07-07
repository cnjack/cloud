/*
 * SystemPage.test.tsx — the cluster-admin Cluster view:
 *   - cluster-admin: renders the snapshot cards (capacity, provider, runner…).
 *   - project-admin: presentation-only gate shows a plain notice, no snapshot.
 *   - error state: a failed getSystem shows the ErrorBlock with a Retry.
 */
import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ApiError, type ApiClient } from '../api/client';
import type { Role } from '../api/config';
import type { SystemInfo } from '../api/types';
import { SystemPage } from './SystemPage';

function snapshot(overrides: Partial<SystemInfo> = {}): SystemInfo {
  return {
    version: { version: '1.4.0', commit: 'abc1234' },
    capacity: { max_concurrent_runs: 4, running: 1, queued: 2, scheduling: 1 },
    guardrails: { run_timeout_seconds: 1800, job_ttl_seconds: 3600 },
    provider: { gitea_enabled: true, gitea_url: 'http://gitea:3000' },
    runner: { image: 'ghcr.io/acme/runner:v1' },
    namespace: 'jcloud',
    launcher: 'kubernetes',
    ...overrides,
  };
}

function renderPage(client: Partial<ApiClient>, role: Role) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client as ApiClient} role={role}>
        <MemoryRouter initialEntries={['/system']}>
          <SystemPage />
        </MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('SystemPage', () => {
  it('renders the snapshot cards for a cluster-admin', async () => {
    const client = { getSystem: vi.fn().mockResolvedValue(snapshot()) };
    renderPage(client, 'cluster-admin');

    await waitFor(() => expect(screen.getByTestId('system-cards')).toBeTruthy());
    expect(screen.getByText('Capacity')).toBeTruthy();
    expect(screen.getByText('Guardrails')).toBeTruthy();
    // Runner image surfaces.
    expect(screen.getByText('ghcr.io/acme/runner:v1')).toBeTruthy();
    // Provider enabled pill.
    expect(screen.getByTestId('provider-status').textContent).toContain('enabled');
  });

  it('shows the presentation-only gate notice for a project-admin (no snapshot fetch)', () => {
    const getSystem = vi.fn();
    renderPage({ getSystem }, 'project-admin');

    expect(screen.getByTestId('system-forbidden')).toBeTruthy();
    // The gate is client-side: we don't even call getSystem for a project-admin.
    expect(getSystem).not.toHaveBeenCalled();
  });

  it('shows an error state with Retry when the snapshot fails', async () => {
    const client = {
      getSystem: vi.fn().mockRejectedValue(new ApiError(500, 'boom')),
    };
    renderPage(client, 'cluster-admin');

    await waitFor(() =>
      expect(screen.getByText("Couldn't load the cluster snapshot")).toBeTruthy(),
    );
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();
  });

  it('shows unlimited concurrency when max_concurrent_runs is 0 (no bar)', async () => {
    const client = {
      getSystem: vi
        .fn()
        .mockResolvedValue(
          snapshot({
            capacity: { max_concurrent_runs: 0, running: 3, queued: 0, scheduling: 0 },
          }),
        ),
    };
    renderPage(client, 'cluster-admin');

    await waitFor(() => expect(screen.getByTestId('system-cards')).toBeTruthy());
    expect(screen.getByText('unlimited concurrency')).toBeTruthy();
    // The progressbar is omitted when concurrency is unlimited.
    expect(screen.queryByRole('progressbar')).toBeNull();
  });
});
