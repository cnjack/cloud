import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { AutomationExecution, Project, ProjectAutomationSpec } from '../api/types';
import { AutomationDetailPage } from './AutomationDetailPage';

const spec: ProjectAutomationSpec = {
  automation: {
    id: 'auto-1',
    service_id: 'svc-1',
    name: 'Weekday issue sweep',
    trigger_kind: 'cron',
    prompt_template: 'Triage issues',
    enabled: true,
    ignore_jcode: true,
    created_at: '2026-07-01T00:00:00Z',
    updated_at: '2026-07-01T00:00:00Z',
  },
  cron: { cron_expr: '0 9 * * 1-5', output_mode: 'create_card' },
};

const blocked: AutomationExecution = {
  id: 'execution-blocked',
  automation_id: 'auto-1',
  automation_name: spec.automation.name,
  trigger_kind: 'cron',
  state: 'blocked',
  output_mode: 'create_card',
  reason_code: 'jtype_unavailable',
  reason: 'Reconnect the Project JType plugin.',
  repair_role: 'project_owner',
  requested_actor: null,
  accountable_actor: { kind: 'cloud_user', id: 'owner', label: 'Ada' },
  output: { kind: 'none', label: 'No output', available: false },
  run: null,
  card: null,
  writeback_state: '',
  usage_summary: {
    availability: 'unavailable',
    reason: 'no_requests',
    requests: 0,
    capture: { reported: 0, partial: 0, unavailable: 0, parse_error: 0 },
    tokens: { input: null, output: null, cache_read: null, cache_write: null },
    costs: { reported: [], estimated: [], uncosted: [] },
  },
  created_at: '2026-07-31T09:00:00Z',
  updated_at: '2026-07-31T09:00:00Z',
};

function renderPage(options: {
  role?: 'owner' | 'member' | 'viewer';
  items?: AutomationExecution[];
  runAutomationNow?: ApiClient['runAutomationNow'];
} = {}) {
  const project: Project = {
    id: 'p1', name: 'Payments', role: options.role ?? 'member', created_at: '',
    services: [{
      id: 'svc-1', project_id: 'p1', name: 'API', repo_kind: 'raw',
      raw_repo_url: 'https://example.invalid/repo.git', default_branch: 'main',
      git_mode: 'readonly', created_at: '',
    }],
  };
  const client = {
    getProject: async () => project,
    getProjectAutomation: async () => spec,
    listAutomationExecutions: async () => ({ items: options.items ?? [blocked], next_cursor: null }),
    runAutomationNow: options.runAutomationNow ?? vi.fn(async () => blocked),
  } as unknown as ApiClient;
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <MemoryRouter initialEntries={['/projects/p1/automations/auto-1']}>
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client}>
          <Routes>
            <Route path="/projects/:projectId/automations/:automationId" element={<AutomationDetailPage />} />
          </Routes>
        </ApiProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe('AutomationDetailPage', () => {
  it('keeps blocked reason, accountability, output, and unavailable usage distinct', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { name: 'Weekday issue sweep' })).toBeTruthy();
    expect(screen.getAllByText('Blocked before output').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Reconnect the Project JType plugin.')).toHaveLength(2);
    expect(screen.getByText('Ada')).toBeTruthy();
    expect(screen.getByText('jtype Card → Kanban Run')).toBeTruthy();
    expect(screen.getByText('Unavailable')).toBeTruthy();
    expect(screen.queryByText('0 tokens')).toBeNull();
  });

  it('keeps the same idempotency key when Run now is retried after a network failure', async () => {
    const calls: string[] = [];
    const runNow = vi.fn(async (_automationId: string, key: string) => {
      calls.push(key);
      if (calls.length === 1) throw new Error('network');
      return blocked;
    });
    renderPage({ runAutomationNow: runNow });
    const button = await screen.findByRole('button', { name: 'Run now' });
    fireEvent.click(button);
    await screen.findByRole('alert');
    fireEvent.click(button);
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[0]).toBe(calls[1]);
  });

  it('lets a Viewer inspect history but not trigger a new execution', async () => {
    renderPage({ role: 'viewer' });
    const button = await screen.findByRole('button', { name: 'Run now' }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(screen.getAllByText('Reconnect the Project JType plugin.')).toHaveLength(2);
  });
});
