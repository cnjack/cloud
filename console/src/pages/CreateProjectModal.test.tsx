/*
 * CreateProjectModal — the "dumb UX" new-project form (M4, blueprint §5).
 * Verifies:
 *  - two fields (name + repo) submit git_mode=readonly by default
 *  - the Draft PR toggle submits git_mode=draft_pr with NO provider fields (the
 *    server smart-parses the URL)
 *  - Draft PR against a raw repo (git://) is blocked inline
 *  - a draft_pr repo whose provider the user hasn't linked shows a Link prompt
 *  - the repo helper copy is accurate (F5)
 */
import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import type { ApiClient } from '../api/client';
import type { AuthProviderInfo, CreateProjectInput, Me, Project } from '../api/types';
import { CreateProjectModal } from './CreateProjectModal';

function makeClient(): { client: ApiClient; created: CreateProjectInput[] } {
  const created: CreateProjectInput[] = [];
  const client: Partial<ApiClient> = {
    createProject: async (input: CreateProjectInput) => {
      created.push(input);
      return {
        id: 'p_new',
        name: input.name,
        repo_url: input.repo_url,
        default_branch: input.default_branch ?? 'main',
        created_at: '2026-07-07T00:00:00Z',
        git_mode: input.git_mode ?? 'readonly',
      } as Project;
    },
  };
  return { client: client as ApiClient, created };
}

function renderModal(
  client: ApiClient,
  opts: { me?: Me | null; providers?: AuthProviderInfo[]; onCreated?: () => void } = {},
) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onCreated = opts.onCreated ?? vi.fn();
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <CreateProjectModal
            open
            onClose={vi.fn()}
            onCreated={onCreated}
            me={opts.me ?? null}
            providers={opts.providers ?? []}
          />
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
  return { onCreated };
}

const fill = (testId: string, value: string) =>
  fireEvent.change(screen.getByTestId(testId), { target: { value } });

describe('CreateProjectModal — simplified form (M4)', () => {
  it('submits git_mode=readonly by default with no provider fields', async () => {
    const { client, created } = makeClient();
    const { onCreated } = renderModal(client);

    fill('project-name-input', 'demo');
    fill('project-repo-input', 'https://gitea.local/acme/demo.git');
    fireEvent.click(screen.getByTestId('create-project-submit'));

    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    expect(created).toHaveLength(1);
    expect(created[0]).toEqual({
      name: 'demo',
      repo_url: 'https://gitea.local/acme/demo.git',
      git_mode: 'readonly',
    });
  });

  it('submits git_mode=draft_pr with no provider fields (server smart-parses URL)', async () => {
    const { client, created } = makeClient();
    const { onCreated } = renderModal(client);

    fill('project-name-input', 'seed');
    fill('project-repo-input', 'https://github.com/jcloud/seed');
    fireEvent.click(screen.getByTestId('git-mode-draft_pr'));
    fireEvent.click(screen.getByTestId('create-project-submit'));

    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    expect(created[0]).toEqual({
      name: 'seed',
      repo_url: 'https://github.com/jcloud/seed',
      git_mode: 'draft_pr',
    });
  });

  it('blocks a Draft PR against a raw (git://) repo before submit', async () => {
    const { client, created } = makeClient();
    renderModal(client);

    fill('project-name-input', 'seed');
    fill('project-repo-input', 'git://seed.internal/seed.git');
    fireEvent.click(screen.getByTestId('git-mode-draft_pr'));
    fireEvent.click(screen.getByTestId('create-project-submit'));

    await waitFor(() => expect(screen.getByText(/provider repository URL/i)).toBeTruthy());
    expect(created).toHaveLength(0);
  });

  it('prompts to link the provider when draft_pr targets an unlinked provider', () => {
    const { client } = makeClient();
    const me: Me = {
      user: { display_name: 'Grace', is_cluster_admin: false },
      is_service: false,
      identities: [], // no gitea identity linked
    };
    const providers: AuthProviderInfo[] = [
      { id: 'gitea', name: 'Gitea', login_url: '/auth/login/gitea' },
    ];
    renderModal(client, { me, providers });

    fill('project-repo-input', 'https://gitea.local/acme/demo.git');
    // Readonly → no prompt.
    expect(screen.queryByTestId('link-prompt')).toBeNull();
    fireEvent.click(screen.getByTestId('git-mode-draft_pr'));

    expect(screen.getByTestId('link-prompt')).toBeTruthy();
    const btn = screen.getByTestId('link-provider-btn');
    expect(btn.getAttribute('href')).toBe('/auth/link/gitea');
  });

  it('does not prompt to link when the user already linked the provider', () => {
    const { client } = makeClient();
    const me: Me = {
      user: { display_name: 'Ada', is_cluster_admin: true },
      is_service: false,
      identities: [{ provider: 'gitea', username: 'ada' }],
    };
    renderModal(client, {
      me,
      providers: [{ id: 'gitea', name: 'Gitea', login_url: '/auth/login/gitea' }],
    });

    fill('project-repo-input', 'https://gitea.local/acme/demo.git');
    fireEvent.click(screen.getByTestId('git-mode-draft_pr'));
    expect(screen.queryByTestId('link-prompt')).toBeNull();
  });

  it('never prompts the console-token service principal to link', () => {
    const { client } = makeClient();
    const me: Me = {
      user: { display_name: 'console token', is_cluster_admin: true },
      is_service: true,
      identities: [],
    };
    renderModal(client, {
      me,
      providers: [{ id: 'gitea', name: 'Gitea', login_url: '/auth/login/gitea' }],
    });

    fill('project-repo-input', 'https://gitea.local/acme/demo.git');
    fireEvent.click(screen.getByTestId('git-mode-draft_pr'));
    expect(screen.queryByTestId('link-prompt')).toBeNull();
  });

  it('uses accurate repo helper copy (F5), not the misleading claim', () => {
    const { client } = makeClient();
    renderModal(client);
    expect(screen.queryByText(/never leaves your domain/i)).toBeNull();
    expect(screen.getByText(/ephemeral workspace/i)).toBeTruthy();
  });
});
