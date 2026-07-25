import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import type { ApiClient } from '../api/client';
import type { Project, ProjectPlugin, ProviderKind } from '../api/types';
import { ProjectPluginsPanel } from './ProjectPluginsPanel';

const project: Project = { id: 'p1', name: 'Demo', created_at: '2026-07-25T00:00:00Z', role: 'owner' };

function renderPanel(
  plugins: ProjectPlugin[],
  role: Project['role'] = 'owner',
  overrides: Partial<ApiClient> = {},
) {
  const client = {
    listProjectPlugins: async () => plugins,
    listGitHubAppInstallations: async () => [],
    getProviderCapabilities: async (provider: ProviderKind) => ({
      provider,
      instance_url: provider === 'github' ? 'https://github.com' : `https://${provider}.example`,
      oauth_scopes: provider === 'gitlab' ? ['read_user', 'api'] : [],
      capabilities: [],
    }),
    ...overrides,
  } as unknown as ApiClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<MemoryRouter><QueryClientProvider client={queryClient}><ApiProvider client={client}><ToastProvider><ProjectPluginsPanel project={{ ...project, role }} /></ToastProvider></ApiProvider></QueryClientProvider></MemoryRouter>);
  return client;
}

describe('ProjectPluginsPanel', () => {
  it('always renders the fixed four provider cards and exposes an independent detail route', async () => {
    renderPanel([{ provider: 'github', status: 'enabled', account_name: 'octo-org', scopes: ['contents'], service_count: 2, automation_count: 1 }]);
    await waitFor(() => expect(screen.getByTestId('plugin-card-github')).toBeTruthy());
    expect(screen.getByTestId('plugin-card-gitlab')).toBeTruthy();
    expect(screen.getByTestId('plugin-card-gitea')).toBeTruthy();
    expect(screen.getByTestId('plugin-card-jtype')).toBeTruthy();
    expect(screen.getByRole('link', { name: /github/i }).getAttribute('href')).toBe('/projects/p1/plugins/github');
    expect(screen.getByText('octo-org')).toBeTruthy();
  });

  it('requires explicit consent acknowledgement before an owner can continue', async () => {
    renderPanel([]);
    await waitFor(() => expect(screen.getAllByText('Connect').length).toBe(4));
    fireEvent.click(screen.getAllByText('Connect')[1]!);
    expect(await screen.findByText('https://gitlab.example')).toBeTruthy();
    expect(screen.getByText('read_user')).toBeTruthy();
    expect(screen.getByText('api')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('broader read/write API operations');
    const continueButton = screen.getByText('Continue to Provider').closest('button')!;
    expect(continueButton.hasAttribute('disabled')).toBe(true);
    fireEvent.click(screen.getByLabelText('I understand and approve this Project-wide access.'));
    expect(continueButton.hasAttribute('disabled')).toBe(false);
  });

  it('requires consent before explicitly selecting a GitHub App Installation', async () => {
    const selectGitHubAppInstallation = vi.fn(async () => ({
      id: 'plugin-gh',
      provider: 'github' as const,
      status: 'enabled' as const,
      scopes: ['github-app'],
    }));
    renderPanel([], 'owner', {
      listGitHubAppInstallations: async () => [{
        id: '12345',
        account_id: '99',
        account: 'acme',
        target_type: 'Organization',
        repository_selection: 'selected',
      }],
      previewGitHubAppInstallationConsent: async () => ({
        installation_id: '12345',
        account: 'acme',
        scopes: ['contents:write', 'repository_selection:selected'],
        repository_selection: 'selected',
        scope_digest: 'scope-digest',
      }),
      selectGitHubAppInstallation,
    });

    await waitFor(() => expect(screen.getAllByText('Connect').length).toBe(4));
    fireEvent.click(screen.getAllByText('Connect')[0]!);
    fireEvent.click(await screen.findByText('acme'));
    await screen.findByText('Actual permissions for acme');
    fireEvent.click(screen.getByLabelText(/I reviewed these actual GitHub App permissions/));
    fireEvent.click(screen.getByText('Connect this Installation'));
    await waitFor(() => expect(selectGitHubAppInstallation).toHaveBeenCalledWith(
      'p1',
      '12345',
      expect.objectContaining({
        consent_version: 'plugin-platform-v2-coarse-scope',
        consent_accepted: true,
        scope_digest: 'scope-digest',
      }),
    ));
  });

  it('starts and polls the JType device consent flow without exposing a token', async () => {
    let plugins: ProjectPlugin[] = [];
    const startPluginInstall = vi.fn(async () => {
      plugins = [{
        id: 'plugin-jtype',
        provider: 'jtype',
        status: 'connecting',
        scopes: ['full'],
      }];
      return {
        connect_id: 'connect-1',
        user_code: 'JTYP-42',
        verification_uri: 'https://jtype.example/device',
        verification_uri_complete: 'https://jtype.example/device?user_code=JTYP-42',
        expires_in: 600,
        interval: 2,
      };
    });
    const getJTypePluginConnectStatus = vi.fn(async () => ({
      status: 'complete' as const,
      token_set: true,
    }));
    renderPanel([], 'owner', {
      listProjectPlugins: async () => plugins,
      startPluginInstall,
      getJTypePluginConnectStatus,
    });

    await waitFor(() => expect(screen.getAllByText('Connect').length).toBe(4));
    fireEvent.click(screen.getAllByText('Connect')[3]!);
    fireEvent.click(screen.getByLabelText('I understand and approve this Project-wide access.'));
    fireEvent.click(screen.getByText('Continue to Provider'));

    expect(await screen.findByText('JTYP-42')).toBeTruthy();
    await waitFor(() => expect(getJTypePluginConnectStatus).toHaveBeenCalledWith('p1', 'plugin-jtype', 'connect-1'));
    expect(screen.queryByText(/access[_ ]token/i)).toBeNull();
  });

  it('keeps connection controls hidden for members while preserving visibility', async () => {
    renderPanel([], 'member');
    await waitFor(() => expect(screen.getByTestId('plugin-card-github')).toBeTruthy());
    expect(screen.queryByText('Connect')).toBeNull();
    expect(screen.getByText('Only a Project owner or Cluster administrator can change Plugin connections.')).toBeTruthy();
  });
});
