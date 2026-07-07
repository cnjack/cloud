/*
 * CreateProjectModal.test.tsx — F3 (git integration in create) + F5 (copy).
 * Verifies:
 *  - default submit sends git_mode=readonly (no provider fields)
 *  - selecting Draft PR reveals provider fields, validates provider_repo, and
 *    submits the full draft_pr payload (provider=gitea + provider_repo/url)
 *  - the repo hint uses the accurate copy, not the misleading "never leaves"
 */
import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import type { ApiClient } from '../api/client';
import type { CreateProjectInput, Project } from '../api/types';
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
        default_branch: input.default_branch,
        created_at: '2026-07-07T00:00:00Z',
        git_mode: input.git_mode ?? 'readonly',
        provider: input.provider ?? '',
        provider_url: input.provider_url ?? '',
        provider_repo: input.provider_repo ?? '',
      } as Project;
    },
  };
  return { client: client as ApiClient, created };
}

function renderModal(client: ApiClient, onCreated = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <CreateProjectModal open onClose={vi.fn()} onCreated={onCreated} />
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
  return { onCreated };
}

const fill = (testId: string, value: string) =>
  fireEvent.change(screen.getByTestId(testId), { target: { value } });

describe('CreateProjectModal — git integration (F3) + copy (F5)', () => {
  it('submits git_mode=readonly by default with no provider fields', async () => {
    const { client, created } = makeClient();
    const { onCreated } = renderModal(client);

    fill('project-name-input', 'demo');
    fill('project-repo-input', 'https://gitea.local/acme/demo.git');
    fireEvent.click(screen.getByTestId('create-project-submit'));

    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    expect(created).toHaveLength(1);
    expect(created[0]).toMatchObject({
      name: 'demo',
      repo_url: 'https://gitea.local/acme/demo.git',
      git_mode: 'readonly',
    });
    expect(created[0]!.provider_repo).toBeUndefined();
  });

  it('reveals provider fields and submits the draft_pr payload', async () => {
    const { client, created } = makeClient();
    const { onCreated } = renderModal(client);

    fill('project-name-input', 'seed');
    fill('project-repo-input', 'https://gitea.local/jcloud/seed.git');

    // Draft PR fields are hidden until the mode is selected.
    expect(screen.queryByTestId('draft-pr-fields')).toBeNull();
    fireEvent.click(screen.getByTestId('git-mode-draft_pr'));
    expect(screen.getByTestId('draft-pr-fields')).toBeTruthy();

    fill('provider-repo-input', 'jcloud/seed');
    fill('provider-url-input', 'http://gitea.internal:3000');
    fireEvent.click(screen.getByTestId('create-project-submit'));

    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    expect(created[0]).toMatchObject({
      git_mode: 'draft_pr',
      provider: 'gitea',
      provider_repo: 'jcloud/seed',
      provider_url: 'http://gitea.internal:3000',
    });
  });

  it('blocks submit with an invalid provider_repo shape', async () => {
    const { client, created } = makeClient();
    renderModal(client);

    fill('project-name-input', 'seed');
    fill('project-repo-input', 'https://gitea.local/jcloud/seed.git');
    fireEvent.click(screen.getByTestId('git-mode-draft_pr'));
    fill('provider-repo-input', 'not-a-valid-repo');
    fireEvent.click(screen.getByTestId('create-project-submit'));

    await waitFor(() =>
      expect(screen.getByText(/owner\/name/i)).toBeTruthy(),
    );
    expect(created).toHaveLength(0);
  });

  it('uses accurate repo helper copy (F5), not the misleading claim', () => {
    const { client } = makeClient();
    renderModal(client);
    expect(screen.queryByText(/never leaves your domain/i)).toBeNull();
    expect(screen.getByText(/ephemeral workspace/i)).toBeTruthy();
  });
});
