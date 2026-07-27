import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { CreateProjectAutomationInput, Project, ProjectAutomationSpec } from '../api/types';
import { AutomationEditorPage } from './AutomationEditorPage';

const project: Project = {
  id: 'p1',
  name: 'Demo',
  role: 'member',
  created_at: '',
  services: [{
    id: 'svc-1',
    project_id: 'p1',
    name: 'API',
    repo_kind: 'provider',
    provider: 'gitea',
    repo_owner_name: 'acme/api',
    default_branch: 'main',
    git_mode: 'draft_pr',
    created_at: '',
  }],
};

function result(input: CreateProjectAutomationInput): ProjectAutomationSpec {
  return {
    automation: {
      id: 'auto-1',
      service_id: input.service_id,
      name: input.name,
      trigger_kind: input.scm ? 'scm' : input.kanban ? 'kanban' : 'cron',
      prompt_template: input.prompt_template,
      enabled: input.enabled ?? true,
      ignore_jcode: input.ignore_jcode ?? true,
      created_at: '',
      updated_at: '',
    },
    scm: input.scm,
    actions: input.scm?.actions,
    kanban: input.kanban,
    cron: input.cron,
  };
}

function renderEditor(overrides: Partial<ApiClient> = {}) {
  const create = vi.fn(async (_projectId: string, input: CreateProjectAutomationInput) => result(input));
  const client = {
    getProject: async () => project,
    listProjectPlugins: async () => [],
    getProviderCapabilities: async () => ({
      provider: 'gitea',
      minimum_version: '1.25',
      capabilities: [
        { family: 'push', actions: ['updated'] },
        { family: 'pull_request', actions: ['opened', 'reopened', 'synchronized', 'closed', 'merged'] },
        { family: 'review', actions: ['approved', 'changes_requested', 'commented'] },
        { family: 'comment', actions: ['created'] },
        { family: 'issue', actions: ['opened', 'reopened', 'updated', 'closed'] },
        { family: 'check', actions: ['completed'] },
        { family: 'tag', actions: ['created', 'deleted'] },
        { family: 'release', actions: ['published', 'updated', 'deleted'] },
      ],
    }),
    listProjectModels: async () => ({
      models: [
        {
          id: 'glm-52',
          name: 'GLM 5.2',
          model_name: 'zhipuai-coding-plan/glm-5.2',
          capabilities: { reasoning: true, tools: true, image: false },
        },
        {
          id: 'fast',
          name: 'Fast',
          model_name: 'provider/fast',
          capabilities: { reasoning: false, tools: true, image: false },
        },
      ],
      env_fallback: false,
    }),
    createProjectAutomation: create,
    ...overrides,
  } as unknown as ApiClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <MemoryRouter initialEntries={['/projects/p1/automations/new?service=svc-1']}>
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client}>
          <Routes>
            <Route path="/projects/:projectId/automations/new" element={<AutomationEditorPage />} />
            <Route path="/projects/:projectId" element={<div>Project destination</div>} />
          </Routes>
        </ApiProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return { create };
}

describe('AutomationEditorPage', () => {
  it('sends the exact strongly typed SCM request and gates unsupported Gitea actions', async () => {
    const { create } = renderEditor();
    await screen.findByRole('heading', { name: 'Create Automation' });
    expect(screen.queryByRole('combobox', { name: 'Service' })).toBeNull();
    expect(screen.getByText('API')).toBeTruthy();
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'glm-52' } });
    fireEvent.change(screen.getByLabelText('Reasoning effort'), { target: { value: 'high' } });

    fireEvent.click(screen.getByText('More events'));
    const unsupported = screen.getByLabelText(/pull_request.ready/i) as HTMLInputElement;
    expect(unsupported.disabled).toBe(true);
    expect((screen.getByLabelText('Path pattern') as HTMLInputElement).disabled).toBe(false);
    expect((screen.getByLabelText('CI conclusion') as HTMLInputElement).disabled).toBe(true);
    expect((await screen.findAllByText(/Not supported by gitea 1\.25\+\./)).length).toBeGreaterThan(0);

    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'PR guard' } });
    fireEvent.change(screen.getByLabelText('Prompt template'), { target: { value: 'Review the change.' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create Automation' }));

    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0]?.[1]).toEqual({
      service_id: 'svc-1',
      name: 'PR guard',
      prompt_template: 'Review the change.',
      model_id: 'glm-52',
      model_effort: 'high',
      enabled: true,
      ignore_jcode: true,
      scm: {
        branch: '',
        path_pattern: '',
        conclusion: '',
        actions: [{ event_family: 'push', action: 'updated' }],
      },
    });
  });

  it('hides effort when the selected model does not advertise reasoning support', async () => {
    renderEditor();
    await screen.findByRole('heading', { name: 'Create Automation' });
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'fast' } });
    expect(screen.queryByLabelText('Reasoning effort')).toBeNull();
  });

  it('keeps JType Kanban out of the Automation editor', async () => {
    const { create } = renderEditor();
    await screen.findByRole('heading', { name: 'Create Automation' });
    expect(screen.queryByRole('button', { name: 'JType Kanban' })).toBeNull();
    expect(screen.getByRole('button', { name: 'SCM' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Cron' })).toBeTruthy();
    expect(create).not.toHaveBeenCalled();
  });

  it('keeps common SCM events visible and places the complete low-frequency matrix behind More events', async () => {
    renderEditor();
    await screen.findByRole('heading', { name: 'Create Automation' });

    expect(screen.getByLabelText('push.updated')).toBeTruthy();
    expect(screen.getByLabelText('pull_request.opened')).toBeTruthy();
    expect(screen.getByLabelText('issue.opened')).toBeTruthy();
    expect(screen.getByLabelText('issue.updated')).toBeTruthy();
    expect(screen.queryByLabelText('release.deleted')).toBeNull();

    fireEvent.click(screen.getByText('More events'));
    expect(screen.getByLabelText('release.deleted')).toBeTruthy();
    expect(screen.getByLabelText('issue.closed')).toBeTruthy();
  });
});
