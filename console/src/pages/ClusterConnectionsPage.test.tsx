import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { KanbanClusterConfig, SystemInfo } from '../api/types';
import { ToastProvider } from '../components/Toast';
import { ClusterConnectionsPage } from './ClusterConnectionsPage';

const system: SystemInfo = {
  version: { version: '1.4.0', commit: 'abc' },
  capacity: { max_concurrent_runs: 4, running: 0, scheduling: 0, queued: 0 },
  guardrails: { run_timeout_seconds: 1800, job_ttl_seconds: 3600 },
  provider: { gitea_enabled: true, gitea_url: 'https://gitea.example', allowed_git_hosts: ['gitea.example'] },
  runner: { image: 'runner:v1', persistent_workspace: true }, namespace: 'jcode', launcher: 'kubernetes',
  auth: { providers: ['gitea'], users_count: 14 },
  archive: { enabled: false, reason: 'S3_ARCHIVE_BUCKET is not configured' },
};

const kanban: KanbanClusterConfig = {
  base_url: 'https://jtype.example', token_set: true, source: 'db', effective_enabled: true,
  effective_base_url: 'https://jtype.example', cluster_token_set: true, poll_interval: '15s',
};

describe('ClusterConnectionsPage', () => {
  it('renders effective connections and the visible unavailable archive dependency', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const client = { getSystem: async () => system, getKanbanConfig: async () => kanban } as ApiClient;
    render(
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client} role="cluster-admin">
          <ToastProvider><MemoryRouter><ClusterConnectionsPage /></MemoryRouter></ToastProvider>
        </ApiProvider>
      </QueryClientProvider>,
    );
    await screen.findByText('jtype Kanban');
    expect(screen.getByDisplayValue('https://jtype.example')).toBeTruthy();
    expect(screen.getByText('S3_ARCHIVE_BUCKET is not configured')).toBeTruthy();
    expect(screen.getByText('Gitea OAuth')).toBeTruthy();
  });
});
