/*
 * ProjectDetailPage — M4 composer + role gating (blueprint §5):
 *  - single-repo project: composer, no repository selector; runs use the project
 *    shim (createRun)
 *  - multi-repo project: composer shows a repository selector; runs dispatch
 *    against the selected service (createServiceRun)
 *  - viewer: no composer, no Settings, no "+ Add repository"
 *  - owner: "+ Add repository" opens a form that creates a service
 */
import { describe, expect, it } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ApiProvider } from '../api/ApiProvider';
import { ToastProvider } from '../components/Toast';
import type { ApiClient } from '../api/client';
import type {
  CreateRunInput,
  CreateServiceInput,
  MemberRole,
  Project,
  Run,
  Service,
} from '../api/types';
import { ProjectDetailPage } from './ProjectDetailPage';

function svc(id: string, name: string): Service {
  return {
    id,
    project_id: 'p1',
    name,
    repo_kind: 'provider',
    provider: 'gitea',
    repo_owner_name: `acme/${name}`,
    default_branch: 'main',
    git_mode: 'readonly',
    created_at: '',
  };
}

function project(role: MemberRole, services: Service[]): Project {
  return {
    id: 'p1',
    name: 'demo',
    repo_url: 'https://gitea.local/acme/demo.git',
    default_branch: 'main',
    created_at: '',
    git_mode: 'readonly',
    role,
    services,
  };
}

interface Calls {
  runs: { pid: string; input: CreateRunInput }[];
  serviceRuns: { sid: string; input: CreateRunInput }[];
  services: { pid: string; input: CreateServiceInput }[];
}

function makeClient(p: Project): { client: ApiClient; calls: Calls } {
  const calls: Calls = { runs: [], serviceRuns: [], services: [] };
  const client: Partial<ApiClient> = {
    getProject: async () => p,
    listRuns: async () => [] as Run[],
    createRun: async (pid, input) => {
      calls.runs.push({ pid, input });
      return { id: 'r1', project_id: pid, prompt: input.prompt, status: 'queued', created_at: '' } as Run;
    },
    createServiceRun: async (sid, input) => {
      calls.serviceRuns.push({ sid, input });
      return { id: 'r2', project_id: 'p1', service_id: sid, prompt: input.prompt, status: 'queued', created_at: '' } as Run;
    },
    createService: async (pid, input) => {
      calls.services.push({ pid, input });
      return svc('svc_new', input.name ?? 'default');
    },
  };
  return { client: client as ApiClient, calls };
}

function renderPage(client: ApiClient) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ApiProvider client={client}>
        <ToastProvider>
          <MemoryRouter initialEntries={['/projects/p1']}>
            <Routes>
              <Route path="/projects/:projectId" element={<ProjectDetailPage />} />
              <Route path="/runs/:id" element={<div data-testid="run-page" />} />
              <Route path="/" element={<div data-testid="home" />} />
            </Routes>
          </MemoryRouter>
        </ToastProvider>
      </ApiProvider>
    </QueryClientProvider>,
  );
}

describe('ProjectDetailPage — single-repo composer', () => {
  it('has no repository selector and dispatches via the project shim', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('run-input')).toBeTruthy());
    expect(screen.queryByTestId('composer-service-select')).toBeNull();
    expect(screen.getByTestId('project-settings-btn')).toBeTruthy();

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'do a thing' } });
    fireEvent.click(screen.getByTestId('run-submit'));

    await waitFor(() => expect(calls.runs).toHaveLength(1));
    expect(calls.runs[0]).toMatchObject({ pid: 'p1', input: { prompt: 'do a thing' } });
    expect(calls.serviceRuns).toHaveLength(0);
  });
});

describe('ProjectDetailPage — multi-repo composer', () => {
  it('shows a repository selector and dispatches against the selected service', async () => {
    const services = [svc('svc_default', 'default'), svc('svc_web', 'web')];
    const { client, calls } = makeClient(project('owner', services));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('composer-service-select')).toBeTruthy());

    fireEvent.change(screen.getByTestId('run-input'), { target: { value: 'ship it' } });
    fireEvent.click(screen.getByTestId('run-submit'));

    await waitFor(() => expect(calls.serviceRuns).toHaveLength(1));
    // Defaults to the 'default' service.
    expect(calls.serviceRuns[0]).toMatchObject({ sid: 'svc_default', input: { prompt: 'ship it' } });
    expect(calls.runs).toHaveLength(0);
  });
});

describe('ProjectDetailPage — viewer gating', () => {
  it('hides the composer, Settings and Add repository for a viewer', async () => {
    const { client } = makeClient(project('viewer', [svc('svc_default', 'default')]));
    renderPage(client);

    // The run list still renders (the empty state); the composer does not.
    await waitFor(() => expect(screen.getByTestId('runs-empty')).toBeTruthy());
    expect(screen.queryByTestId('run-input')).toBeNull();
    expect(screen.queryByTestId('project-settings-btn')).toBeNull();
    expect(screen.queryByTestId('add-repo-trigger')).toBeNull();
  });
});

describe('ProjectDetailPage — add repository', () => {
  it('opens the inline form and creates a service (owner)', async () => {
    const { client, calls } = makeClient(project('owner', [svc('svc_default', 'default')]));
    renderPage(client);

    await waitFor(() => expect(screen.getByTestId('add-repo-trigger')).toBeTruthy());
    fireEvent.click(screen.getByTestId('add-repo-trigger'));

    fireEvent.change(screen.getByTestId('add-repo-name'), { target: { value: 'web' } });
    fireEvent.change(screen.getByTestId('add-repo-url'), {
      target: { value: 'https://github.com/acme/web' },
    });
    fireEvent.click(screen.getByTestId('add-repo-submit'));

    await waitFor(() => expect(calls.services).toHaveLength(1));
    expect(calls.services[0]).toMatchObject({
      pid: 'p1',
      input: { name: 'web', repo_url: 'https://github.com/acme/web', git_mode: 'readonly' },
    });
  });
});
