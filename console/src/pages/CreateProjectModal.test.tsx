/*
 * CreateProjectModal — the new-project form (multitenant blueprint §5).
 * A project is a pure container now: the form is a single Name field and
 * submit POSTs exactly { name } (the orchestrator rejects unknown fields).
 * Repo config (URL / git mode / provider linking) moved to the project page's
 * "+ Add repository" flow — covered in ProjectDetailPage.test.tsx.
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
      const project: Project = {
        id: 'p_new',
        name: input.name,
        created_at: '2026-07-07T00:00:00Z',
        role: 'owner',
        services: [],
      };
      return project;
    },
  };
  return { client: client as ApiClient, created };
}

function renderModal(client: ApiClient, opts: { onCreated?: () => void } = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onCreated = opts.onCreated ?? vi.fn();
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

describe('CreateProjectModal — name-only form', () => {
  it('submits exactly { name } — no repo fields ride on project create', async () => {
    const { client, created } = makeClient();
    const { onCreated } = renderModal(client);

    fill('project-name-input', 'demo');
    fireEvent.click(screen.getByTestId('create-project-submit'));

    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    expect(created).toHaveLength(1);
    expect(created[0]).toEqual({ name: 'demo' });
  });

  it('trims the name before submit', async () => {
    const { client, created } = makeClient();
    const { onCreated } = renderModal(client);

    fill('project-name-input', '  demo  ');
    fireEvent.click(screen.getByTestId('create-project-submit'));

    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    expect(created[0]).toEqual({ name: 'demo' });
  });

  it('blocks an empty name inline before submit', async () => {
    const { client, created } = makeClient();
    const { onCreated } = renderModal(client);

    fireEvent.click(screen.getByTestId('create-project-submit'));

    await waitFor(() => expect(screen.getByText(/name is required/i)).toBeTruthy());
    expect(created).toHaveLength(0);
    expect(onCreated).not.toHaveBeenCalled();
  });

  it('has no repo URL field or git-mode toggle (moved to Add repository)', () => {
    const { client } = makeClient();
    renderModal(client);

    expect(screen.queryByTestId('project-repo-input')).toBeNull();
    expect(screen.queryByTestId('git-mode-control')).toBeNull();
    expect(screen.queryByTestId('link-prompt')).toBeNull();
  });

  it('keeps focus on the name input while typing (does not jump to close button)', () => {
    const { client } = makeClient();
    renderModal(client);

    const input = screen.getByTestId('project-name-input');
    // The modal autofocuses the name field, not the header close button.
    expect(document.activeElement).toBe(input);

    // Each keystroke re-renders the parent (new onClose identity); focus must
    // stay put rather than being yanked back to the first focusable control.
    fill('project-name-input', 'd');
    expect(document.activeElement).toBe(input);
    fill('project-name-input', 'de');
    expect(document.activeElement).toBe(input);
  });
});
