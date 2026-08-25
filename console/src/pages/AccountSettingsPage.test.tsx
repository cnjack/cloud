import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react';
import { Link, MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import { AccountSettingsPage } from './AccountSettingsPage';

vi.mock('../auth/AuthProvider', () => ({
  useOptionalAuth: () => ({
    me: {
      user: { id: 'user-1', display_name: 'Jack', avatar_url: '', is_cluster_admin: true },
      identities: [{ provider: 'github', username: 'cnjack' }],
    },
    providers: [
      { id: 'github', name: 'GitHub', login_url: '/auth/login/github' },
      { id: 'gitea', name: 'Gitea', login_url: '/auth/login/gitea' },
    ],
  }),
}));

function renderPage(initialEntry = '/account/settings') {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = {
    listAccountModels: async () => [{ id: 'model-1', name: 'Codex', model_name: 'gpt-test', capabilities: { reasoning: true, tools: true, image: false } }],
    listAccountRepositories: async () => ({ repositories: [], sources: [{ provider: 'github', account: 'cnjack', status: 'ready' }] }),
  } as unknown as ApiClient;
  return render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client} role="cluster-admin">
        <MemoryRouter initialEntries={[initialEntry]}>
          <Link to="/account/settings?section=usage">External usage link</Link>
          <AccountSettingsPage />
        </MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('AccountSettingsPage', () => {
  it('keeps identity and account-scoped settings out of Repository settings', async () => {
    renderPage();
    expect(screen.getByRole('heading', { name: 'Personal settings' })).toBeTruthy();
    fireEvent.click(screen.getByRole('tab', { name: 'Git accounts' }));
    expect(screen.getByText('github/cnjack')).toBeTruthy();
    expect(screen.getByRole('link', { name: /Link Gitea/ }).getAttribute('href')).toBe('/auth/link/gitea');

    fireEvent.click(screen.getByRole('tab', { name: 'Models' }));
    expect(await screen.findByText('Codex')).toBeTruthy();
    expect(screen.getByText('Authorized for your account')).toBeTruthy();
    expect(screen.queryByRole('checkbox')).toBeNull();
  });

  it('tracks the requested section when account-menu navigation changes the query', async () => {
    renderPage('/account/settings?section=models');
    expect(screen.getByRole('heading', { name: 'Model access' })).toBeTruthy();

    fireEvent.click(screen.getByRole('link', { name: 'External usage link' }));
    expect(screen.getByRole('tab', { name: 'Usage' }).getAttribute('aria-selected')).toBe('true');
  });
});
