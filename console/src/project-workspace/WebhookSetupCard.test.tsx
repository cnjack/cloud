import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ApiError, type ApiClient } from '../api/client';
import type { AuthProviderInfo, Me, Service } from '../api/types';
import { WebhookSetupCard } from './WebhookSetupCard';

const service: Service = {
  id: 'svc-1',
  project_id: 'project-1',
  name: 'orchestrator',
  repo_kind: 'provider',
  provider: 'gitea',
  repo_owner_name: 'jcloud/orchestrator',
  default_branch: 'main',
  git_mode: 'draft_pr',
  created_at: '',
};

const gitea: AuthProviderInfo = {
  id: 'gitea',
  name: 'Gitea',
  login_url: '/auth/login/gitea',
};

const meWithoutGitea: Me = {
  user: { id: 'user-1', display_name: 'Jack', is_cluster_admin: false },
  identities: [{ provider: 'github', username: 'jack' }],
};

const meWithGitea: Me = {
  ...meWithoutGitea,
  identities: [
    ...meWithoutGitea.identities,
    { provider: 'gitea', username: 'jack' },
  ],
};

function renderCard(
  client: ApiClient,
  props: Partial<React.ComponentProps<typeof WebhookSetupCard>> = {},
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>
        <WebhookSetupCard
          service={service}
          me={meWithoutGitea}
          providers={[gitea]}
          canConfigure
          returnTo="/projects/project-1?service=svc-1&tab=automations&webhook=oauth"
          {...props}
        />
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('WebhookSetupCard', () => {
  it('guides a member through provider OAuth without asking for a token', () => {
    renderCard({ ensureServiceWebhook: vi.fn() } as unknown as ApiClient);

    const link = screen.getByTestId('webhook-oauth-connect') as HTMLAnchorElement;
    expect(link.textContent).toContain('Connect Gitea');
    expect(link.getAttribute('href')).toBe(
      '/auth/link/gitea?return_to=%2Fprojects%2Fproject-1%3Fservice%3Dsvc-1%26tab%3Dautomations%26webhook%3Doauth',
    );
    expect(screen.queryByLabelText(/token/i)).toBeNull();
  });

  it('uses the OAuth-backed sync endpoint and only reports success after it returns', async () => {
    const ensureServiceWebhook = vi.fn().mockResolvedValue({
      provider: 'gitea',
      endpoint: 'https://orchestrator.example/webhooks/gitea',
      status: 'synced',
    });
    renderCard({ ensureServiceWebhook } as unknown as ApiClient, { me: meWithGitea });

    fireEvent.click(screen.getByTestId('webhook-sync'));
    await waitFor(() => expect(ensureServiceWebhook).toHaveBeenCalledWith('svc-1'));
    expect((await screen.findByTestId('webhook-setup-success')).textContent).toContain(
      'Gitea webhook is synced',
    );
  });

  it('automatically performs one idempotent sync after returning from OAuth', async () => {
    const ensureServiceWebhook = vi.fn().mockResolvedValue({
      provider: 'gitea',
      endpoint: 'https://orchestrator.example/webhooks/gitea',
      status: 'synced',
    });
    renderCard(
      { ensureServiceWebhook } as unknown as ApiClient,
      { me: meWithGitea, oauthReturned: true },
    );

    await waitFor(() => expect(ensureServiceWebhook).toHaveBeenCalledTimes(1));
    expect((await screen.findByTestId('webhook-setup-success')).textContent).toContain(
      'Gitea webhook is synced',
    );
  });

  it('keeps provider registration failures visible instead of showing a health state', async () => {
    const ensureServiceWebhook = vi
      .fn()
      .mockRejectedValue(new ApiError(502, 'Provider rejected webhook registration.'));
    renderCard({ ensureServiceWebhook } as unknown as ApiClient, { me: meWithGitea });

    fireEvent.click(screen.getByTestId('webhook-sync'));
    expect((await screen.findByTestId('webhook-setup-error')).textContent).toContain(
      'Provider rejected webhook registration.',
    );
    expect(screen.queryByTestId('webhook-setup-success')).toBeNull();
  });

  it('explains why a raw service cannot receive provider review events', () => {
    renderCard({ ensureServiceWebhook: vi.fn() } as unknown as ApiClient, {
      service: { ...service, repo_kind: 'raw', provider: undefined, repo_owner_name: undefined },
    });

    expect(screen.getByTestId('webhook-setup-unavailable').textContent).toContain(
      'provider-backed repository',
    );
  });

  it('does not offer OAuth linking from a service-principal session', () => {
    renderCard({ ensureServiceWebhook: vi.fn() } as unknown as ApiClient, {
      me: { ...meWithoutGitea, is_service: true },
    });

    expect(screen.getByTestId('webhook-setup-unavailable').textContent).toContain(
      'signed-in project member',
    );
    expect(screen.queryByTestId('webhook-oauth-connect')).toBeNull();
  });
});
