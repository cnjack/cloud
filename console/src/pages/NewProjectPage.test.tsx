import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { Project } from '../api/types';
import { ToastProvider } from '../components/Toast';
import { NewProjectPage } from './NewProjectPage';

function renderPage(createProject = vi.fn(async ({ name }: { name: string }) => ({
  id: 'p-new', name, created_at: '2026-07-14T00:00:00Z', services: [],
} satisfies Project))) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = { createProject, listProjects: async () => [] } as unknown as ApiClient;
  render(
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>
        <ToastProvider>
          <MemoryRouter initialEntries={['/projects/new']}>
            <Routes>
              <Route path="/projects/new" element={<NewProjectPage />} />
              <Route path="/projects/:projectId" element={<div data-testid="created-workspace" />} />
            </Routes>
          </MemoryRouter>
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
  return createProject;
}

describe('NewProjectPage', () => {
  it('creates only the ownership container and opens the resulting workspace', async () => {
    const create = renderPage();
    fireEvent.change(screen.getByLabelText(/Name/i), { target: { value: '  Runtime  ' } });
    fireEvent.click(screen.getByRole('button', { name: /Create Project/i }));
    await waitFor(() => expect(create).toHaveBeenCalledWith({ name: 'Runtime' }));
    expect(await screen.findByTestId('created-workspace')).toBeTruthy();
  });

  it('keeps unavailable repository/model dependencies out of project creation', () => {
    renderPage();
    expect(screen.queryByLabelText(/Repository/i)).toBeNull();
    expect(screen.getByText(/No repository is created or connected yet/i)).toBeTruthy();
  });
});
