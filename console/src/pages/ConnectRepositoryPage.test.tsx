import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { Project } from '../api/types';
import { ConnectRepositoryPage } from './ConnectRepositoryPage';

vi.mock('./ProjectPluginsPanel', () => ({
  ProjectPluginsPanel: ({ project }: { project: Project }) => <div data-testid="provider-connections">{project.id}</div>,
}));

function renderPage(client: ApiClient) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>
        <MemoryRouter><ConnectRepositoryPage /></MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('ConnectRepositoryPage', () => {
  it('creates the hidden personal container automatically for Provider accounts', async () => {
    const createProject = vi.fn(async () => ({ id: 'personal', name: 'Personal workspace', role: 'owner', services: [], created_at: '' }));
    renderPage({ listProjects: async () => [], createProject } as unknown as ApiClient);

    await waitFor(() => expect(createProject).toHaveBeenCalledWith({ name: 'Personal workspace' }));
    expect((await screen.findByTestId('provider-connections')).textContent).toContain('personal');
    expect(screen.queryByRole('link', { name: 'Choose repository' })).toBeNull();
    expect(screen.queryByText(/New Project|Service/)).toBeNull();
  });

  it('reuses the existing owner container for Provider connections', async () => {
    renderPage({ listProjects: async () => [{ id: 'personal', name: 'Hidden', role: 'owner', services: [], created_at: '' }] } as unknown as ApiClient);

    expect((await screen.findByTestId('provider-connections')).textContent).toContain('personal');
    expect(screen.queryByRole('link', { name: 'Choose repository' })).toBeNull();
  });
});
