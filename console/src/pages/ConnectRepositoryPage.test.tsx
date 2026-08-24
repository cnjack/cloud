import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { Project } from '../api/types';
import { ConnectRepositoryPage } from './ConnectRepositoryPage';

vi.mock('./ProjectDetailPage', () => ({
  ProjectDetailPage: ({ projectIdOverride, repositoryMode, connectMode }: { projectIdOverride: string; repositoryMode: boolean; connectMode: boolean }) => (
    <div data-testid="repository-picker">{projectIdOverride}:{String(repositoryMode)}:{String(connectMode)}</div>
  ),
}));
vi.mock('./ProjectPluginsPanel', () => ({
  ProjectPluginsPanel: ({ project }: { project: Project }) => <div data-testid="provider-connections">{project.id}</div>,
}));

function renderPage(client: ApiClient, connectionsOnly = false) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>
        <MemoryRouter><ConnectRepositoryPage connectionsOnly={connectionsOnly} /></MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('ConnectRepositoryPage', () => {
  it('creates the hidden personal container automatically and opens the Repository picker', async () => {
    const createProject = vi.fn(async () => ({ id: 'personal', name: 'Personal workspace', role: 'owner', services: [], created_at: '' }));
    renderPage({ listProjects: async () => [], createProject } as unknown as ApiClient);

    await waitFor(() => expect(createProject).toHaveBeenCalledWith({ name: 'Personal workspace' }));
    expect((await screen.findByTestId('repository-picker')).textContent).toContain('personal:true:true');
    expect(screen.queryByText(/New Project|Service/)).toBeNull();
  });

  it('reuses the existing owner container for Provider connections', async () => {
    renderPage({ listProjects: async () => [{ id: 'personal', name: 'Hidden', role: 'owner', services: [], created_at: '' }] } as unknown as ApiClient, true);

    expect((await screen.findByTestId('provider-connections')).textContent).toContain('personal');
    expect(screen.getByRole('link', { name: 'Choose repository' }).getAttribute('href')).toBe('/repositories/connect');
  });
});
