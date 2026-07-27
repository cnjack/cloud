import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { ClusterProviderConfig, ProviderKind, SystemInfo } from '../api/types';
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

const providers: Record<ProviderKind, ClusterProviderConfig> = {
  github: {
    provider: 'github', base_url: 'https://github.com', login_enabled: true,
    plugin_enabled: true, configured: false, health: 'unknown', client_id: 'Iv1.test',
    client_secret_set: true, app_id: '4395162', app_id_set: true,
    app_private_key_set: false, webhook_secret_set: true,
  },
  gitlab: { provider: 'gitlab', base_url: '', login_enabled: false, plugin_enabled: false, configured: false, health: 'unknown' },
  gitea: { provider: 'gitea', base_url: 'https://gitea.example', login_enabled: true, plugin_enabled: true, configured: true, health: 'healthy' },
  jtype: {
    provider: 'jtype', base_url: 'https://jtype.example', login_enabled: false,
    plugin_enabled: true, configured: true, health: 'healthy',
    client_id: 'jcode-cloud', client_secret_set: true,
  },
};

describe('ClusterConnectionsPage', () => {
  it('uses the unified Provider API for all configured providers', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const client = {
      getSystem: async () => system,
      getClusterProviderConfig: async (provider: ProviderKind) => providers[provider],
      updateClusterProviderConfig: async (provider: ProviderKind) => providers[provider],
      testClusterProviderConfig: async (provider: ProviderKind) => providers[provider],
    } as unknown as ApiClient;
    render(
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client} role="cluster-admin">
          <ToastProvider><MemoryRouter><ClusterConnectionsPage /></MemoryRouter></ToastProvider>
        </ApiProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByText('JType Kanban')).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Integration configuration' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Login settings' })).toBeTruthy();
    expect(screen.queryByLabelText('Login enabled')).toBeNull();
    fireEvent.click(screen.getByRole('tab', { name: 'Login settings' }));
    expect(await screen.findAllByLabelText('Login enabled')).toHaveLength(3);
    expect(screen.queryByText('JType Kanban')).toBeNull();
    expect(screen.getByText('S3_ARCHIVE_BUCKET is not configured')).toBeTruthy();
    expect(screen.getByText('Gitea OAuth')).toBeTruthy();
  });

  it('shows and saves the GitHub App write-only configuration fields', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const updateClusterProviderConfig = vi.fn(async (provider: ProviderKind) => providers[provider]);
    const client = {
      getSystem: async () => system,
      getClusterProviderConfig: async (provider: ProviderKind) => providers[provider],
      updateClusterProviderConfig,
      testClusterProviderConfig: async (provider: ProviderKind) => providers[provider],
    } as unknown as ApiClient;
    render(
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client} role="cluster-admin">
          <ToastProvider><MemoryRouter><ClusterConnectionsPage /></MemoryRouter></ToastProvider>
        </ApiProvider>
      </QueryClientProvider>,
    );

    expect(await screen.findByDisplayValue('4395162')).toBeTruthy();
    fireEvent.change(screen.getByLabelText('GitHub App private key'), { target: { value: '-----BEGIN RSA PRIVATE KEY-----\ntest\n-----END RSA PRIVATE KEY-----' } });
    fireEvent.change(screen.getByLabelText('Webhook secret'), { target: { value: 'hook-secret' } });
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' })[0]!);

    await waitFor(() => expect(updateClusterProviderConfig).toHaveBeenCalledWith('github', expect.objectContaining({
      app_id: '4395162',
      app_private_key: expect.stringContaining('BEGIN RSA PRIVATE KEY'),
      webhook_secret: 'hook-secret',
    })));
  });

  it('does not overwrite unsaved GitHub App fields when the Provider query refreshes', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const github = { ...providers.github, app_id: '' };
    const updateClusterProviderConfig = vi.fn(async () => ({ ...github, app_id: '4395162', app_id_set: true }));
    const client = {
      getSystem: async () => system,
      getClusterProviderConfig: async (provider: ProviderKind) => provider === 'github' ? github : providers[provider],
      updateClusterProviderConfig,
      testClusterProviderConfig: async (provider: ProviderKind) => providers[provider],
    } as unknown as ApiClient;
    render(
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client} role="cluster-admin">
          <ToastProvider><MemoryRouter><ClusterConnectionsPage /></MemoryRouter></ToastProvider>
        </ApiProvider>
      </QueryClientProvider>,
    );

    const appID = await screen.findByLabelText('GitHub App ID');
    fireEvent.change(appID, { target: { value: '4395162' } });
    queryClient.setQueryData(['cluster-provider', 'github'], { ...github, config_revision: 2 });

    await waitFor(() => expect((appID as HTMLInputElement).value).toBe('4395162'));
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' })[0]!);
    await waitFor(() => expect(updateClusterProviderConfig).toHaveBeenCalledWith(
      'github',
      expect.objectContaining({ app_id: '4395162' }),
    ));
  });

  it('keeps JType out of the login-only tab', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const updateClusterProviderConfig = vi.fn(async (provider: ProviderKind) => providers[provider]);
    const client = {
      getSystem: async () => system,
      getClusterProviderConfig: async (provider: ProviderKind) => providers[provider],
      updateClusterProviderConfig,
      testClusterProviderConfig: async (provider: ProviderKind) => providers[provider],
    } as unknown as ApiClient;
    render(
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client} role="cluster-admin">
          <ToastProvider><MemoryRouter><ClusterConnectionsPage /></MemoryRouter></ToastProvider>
        </ApiProvider>
      </QueryClientProvider>,
    );

    fireEvent.click(await screen.findByRole('tab', { name: 'Login settings' }));
    await waitFor(() => expect(screen.getAllByLabelText('OAuth client ID')).toHaveLength(3));
    const clientIDs = screen.getAllByLabelText('OAuth client ID');
    const loginToggles = screen.getAllByLabelText('Login enabled');
    expect(clientIDs).toHaveLength(3);
    expect(loginToggles).toHaveLength(3);
    expect(screen.queryByDisplayValue('jcode-cloud')).toBeNull();
  });
});
