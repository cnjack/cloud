import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { SystemInfo } from '../api/types';
import { ClusterOverviewPage } from './ClusterOverviewPage';

const info: SystemInfo = {
  version: { version: '1.4.0', commit: 'abc1234' },
  capacity: { max_concurrent_runs: 8, running: 2, scheduling: 1, queued: 0 },
  guardrails: { run_timeout_seconds: 1800, job_ttl_seconds: 86400 },
  provider: { gitea_enabled: true, gitea_url: 'https://gitea.example', allowed_git_hosts: ['gitea.example'] },
  runner: { image: 'runner:v1', persistent_workspace: true },
  namespace: 'jcode', launcher: 'kubernetes', auth: { providers: ['gitea'], users_count: 14 },
};

function renderPage(role: 'cluster-admin' | 'project-admin', getSystem = vi.fn().mockResolvedValue(info)) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = { getSystem, listModelProviders: async () => [] } as unknown as ApiClient;
  render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client} role={role}>
        <MemoryRouter><ClusterOverviewPage /></MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
  return getSystem;
}

describe('ClusterOverviewPage', () => {
  it('renders the metric strip and runtime snapshot', async () => {
    renderPage('cluster-admin');
    await screen.findByTestId('cluster-overview');
    expect(screen.getByText('37.5%')).toBeTruthy();
    expect(screen.getByText('runner:v1')).toBeTruthy();
    expect(screen.getByText('gitea.example')).toBeTruthy();
  });

  it('explains the project-admin boundary without fetching the snapshot', async () => {
    const getSystem = renderPage('project-admin');
    await waitFor(() => expect(screen.getByTestId('cluster-access-denied')).toBeTruthy());
    expect(getSystem).not.toHaveBeenCalled();
  });
});
