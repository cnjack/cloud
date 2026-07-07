/*
 * ProjectSettingsModal.test.tsx — F4. Covers the PATCH edit flow (only changed
 * fields sent; git_mode flip carried) and the DELETE flow behind a confirm step
 * that fires onDeleted (the caller navigates back to the projects list).
 */
import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import type { ApiClient } from '../api/client';
import type { Project, UpdateProjectInput } from '../api/types';
import { ProjectSettingsModal } from './ProjectSettingsModal';

function baseProject(overrides: Partial<Project> = {}): Project {
  return {
    id: 'p1',
    name: 'demo',
    repo_url: 'https://gitea.local/acme/demo.git',
    default_branch: 'main',
    created_at: '2026-07-07T00:00:00Z',
    git_mode: 'readonly',
    provider: '',
    provider_url: '',
    provider_repo: '',
    ...overrides,
  };
}

interface Ctl {
  patches: { id: string; input: UpdateProjectInput }[];
  deletes: string[];
}

function makeClient(project: Project): { client: ApiClient; ctl: Ctl } {
  const ctl: Ctl = { patches: [], deletes: [] };
  const client: Partial<ApiClient> = {
    updateProject: async (id, input) => {
      ctl.patches.push({ id, input });
      return { ...project, ...input } as Project;
    },
    deleteProject: async (id) => {
      ctl.deletes.push(id);
    },
  };
  return { client: client as ApiClient, ctl };
}

function renderModal(
  client: ApiClient,
  project: Project,
  onDeleted = vi.fn(),
  onClose = vi.fn(),
) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <ProjectSettingsModal
            open
            project={project}
            onClose={onClose}
            onDeleted={onDeleted}
          />
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
  return { onDeleted, onClose };
}

describe('ProjectSettingsModal — edit (PATCH)', () => {
  it('sends only the changed default_branch plus the git_mode', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    const { onClose } = renderModal(client, project);

    fireEvent.change(screen.getByTestId('settings-branch-input'), {
      target: { value: 'dev' },
    });
    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(ctl.patches).toHaveLength(1));
    expect(ctl.patches[0]!.id).toBe('p1');
    // readonly project unchanged in mode → git_mode:'readonly' + branch change.
    expect(ctl.patches[0]!.input).toEqual({
      git_mode: 'readonly',
      default_branch: 'dev',
    });
    expect(onClose).toHaveBeenCalled();
  });

  it('flips readonly → draft_pr and carries provider fields', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    renderModal(client, project);

    fireEvent.click(screen.getByTestId('git-mode-draft_pr'));
    fireEvent.change(screen.getByTestId('provider-repo-input'), {
      target: { value: 'acme/demo' },
    });
    fireEvent.click(screen.getByTestId('project-settings-save'));

    await waitFor(() => expect(ctl.patches).toHaveLength(1));
    expect(ctl.patches[0]!.input).toMatchObject({
      git_mode: 'draft_pr',
      provider: 'gitea',
      provider_repo: 'acme/demo',
    });
    // Unchanged default branch is NOT sent.
    expect(ctl.patches[0]!.input.default_branch).toBeUndefined();
  });

  it('pre-fills existing draft_pr integration', () => {
    const project = baseProject({
      git_mode: 'draft_pr',
      provider: 'gitea',
      provider_repo: 'jcloud/seed',
    });
    const { client } = makeClient(project);
    renderModal(client, project);
    expect(screen.getByTestId('draft-pr-fields')).toBeTruthy();
    expect(
      (screen.getByTestId('provider-repo-input') as HTMLInputElement).value,
    ).toBe('jcloud/seed');
  });
});

describe('ProjectSettingsModal — delete (DELETE + navigate)', () => {
  it('requires a confirm step, then deletes and fires onDeleted', async () => {
    const project = baseProject();
    const { client, ctl } = makeClient(project);
    const { onDeleted } = renderModal(client, project);

    // First click reveals the confirm row; no delete yet.
    fireEvent.click(screen.getByTestId('project-delete'));
    expect(screen.getByTestId('delete-confirm')).toBeTruthy();
    expect(ctl.deletes).toHaveLength(0);

    fireEvent.click(screen.getByTestId('project-delete-confirm'));
    await waitFor(() => expect(onDeleted).toHaveBeenCalled());
    expect(ctl.deletes).toEqual(['p1']);
  });
});
