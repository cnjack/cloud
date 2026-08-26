import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react';
import { Link, MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { AccountRepositorySource } from '../api/types';
import { setLocale } from '../i18n';
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

function renderPage(initialEntry = '/account/settings', sources: AccountRepositorySource[] = [{ provider: 'github', account: 'cnjack', status: 'ready' }]) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = {
    listAccountModels: async () => [{ id: 'model-1', name: 'Codex', model_name: 'gpt-test', capabilities: { reasoning: true, tools: true, image: false } }],
    listAccountRepositories: async () => ({ repositories: [], sources }),
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
  beforeEach(async () => {
    window.localStorage.clear();
    await setLocale('en');
    window.localStorage.clear();
  });

  it('keeps identity and account-scoped settings out of Repository settings', async () => {
    renderPage();
    expect(screen.getByRole('heading', { name: 'Account settings' })).toBeTruthy();
    fireEvent.click(screen.getByRole('tab', { name: 'Git accounts' }));
    expect(screen.getByText('github/cnjack')).toBeTruthy();
    expect(screen.getByRole('link', { name: /Link Gitea/ }).getAttribute('href')).toBe('/auth/link/gitea');
    expect(screen.queryByRole('link', { name: /Reauthorize GitHub/ })).toBeNull();

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

  it('puts a real reauthorization action on an unavailable linked account', async () => {
    renderPage('/account/settings?section=connections', [{
      provider: 'github',
      account: 'cnjack',
      status: 'unavailable',
      message: 'Repository access is unavailable; reconnect this provider account',
    }]);

    const reauthorize = await screen.findByRole('link', { name: 'Reauthorize GitHub' });
    expect(reauthorize.getAttribute('href')).toBe('/auth/link/github');
    expect(screen.getByText('Repository access is unavailable; reconnect this provider account')).toBeTruthy();
  });

  it('localizes the merged Account settings surface in Simplified Chinese', async () => {
    await setLocale('zh-Hans');
    renderPage();

    expect(screen.getByRole('heading', { name: '账号设置' })).toBeTruthy();
    expect(screen.getByRole('link', { name: '返回 Work Home' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: '个人资料' })).toBeTruthy();
    expect(screen.getByText('账号资料')).toBeTruthy();
    expect(screen.getByText('Repository 默认 owner')).toBeTruthy();

    fireEvent.click(screen.getByRole('tab', { name: 'Git 账号' }));
    expect(screen.getByText('已连接到此账号')).toBeTruthy();
    expect(screen.getByRole('link', { name: '关联 Gitea' })).toBeTruthy();

    fireEvent.click(screen.getByRole('tab', { name: '模型' }));
    expect(await screen.findByText('账号已授权')).toBeTruthy();
  });
});
