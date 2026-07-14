import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { Project } from '../api/types';
import { ProjectsPage } from './ProjectsPage';

function renderPage(projects: Project[]) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = { listProjects: async () => projects } as ApiClient;
  return render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>
        <MemoryRouter initialEntries={['/projects']}>
          <ProjectsPage />
        </MemoryRouter>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('ProjectsPage — approved workspace design', () => {
  it('renders service-aware rows and filters by project or service name', async () => {
    renderPage([
      {
        id: 'p-cloud',
        name: 'jcode Cloud',
        created_at: '2026-07-01T00:00:00Z',
        services: [
          { id: 's-console', project_id: 'p-cloud', name: 'console', repo_kind: 'raw', raw_repo_url: 'https://example.com/console.git', default_branch: 'main', git_mode: 'readonly', created_at: '' },
        ],
      },
      { id: 'p-docs', name: 'jcode Docs', created_at: '2026-07-02T00:00:00Z', services: [] },
    ]);

    await screen.findByText('jcode Cloud');
    expect(screen.getByRole('link', { name: /New Project/i }).getAttribute('href')).toBe('/projects/new');
    expect(screen.getByText('console')).toBeTruthy();

    fireEvent.change(screen.getByRole('searchbox', { name: 'Search projects' }), {
      target: { value: 'console' },
    });
    expect(screen.getByText('jcode Cloud')).toBeTruthy();
    expect(screen.queryByText('jcode Docs')).toBeNull();
    expect(screen.getByTestId('project-visible-count').textContent).toContain('1');
  });

  it('renders the honest empty state and routes creation to its own page', async () => {
    renderPage([]);
    await waitFor(() => expect(screen.getByTestId('projects-empty')).toBeTruthy());
    const create = screen.getByRole('link', { name: /Create first Project/i });
    expect(create.getAttribute('href')).toBe('/projects/new');
  });
});
