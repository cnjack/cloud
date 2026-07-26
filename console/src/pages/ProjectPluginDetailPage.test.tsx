import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import type { ProjectPlugin } from '../api/types';
import { ToastProvider } from '../components/Toast';
import { i18n } from '../i18n';
import { ProjectPluginDetailPage } from './ProjectPluginDetailPage';

function renderDetail(plugin: ProjectPlugin, overrides: Partial<ApiClient> = {}) {
  const client = {
    getProject: async () => ({
      id: 'p1',
      name: 'Demo',
      created_at: '2026-07-25T00:00:00Z',
      role: 'owner' as const,
      services: [],
    }),
    listProjectPlugins: async () => [plugin],
    listProjectAutomations: async () => [],
    listPluginWorkspaces: async () => [{ id: 'ws-1', name: 'Docs workspace' }],
    listPluginBoards: async () => [],
    ...overrides,
  } as unknown as ApiClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter initialEntries={['/projects/p1/plugins/jtype']}>
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client}>
          <ToastProvider>
            <Routes>
              <Route path="/projects/:projectId/plugins/:provider" element={<ProjectPluginDetailPage />} />
            </Routes>
          </ToastProvider>
        </ApiProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe('ProjectPluginDetailPage', () => {
  it('finishes a JType installation by binding one workspace', async () => {
    const setProjectPluginWorkspace = vi.fn(async (_installationId: string, workspaceId: string) => ({
      id: 'plugin-jtype',
      provider: 'jtype' as const,
      status: 'enabled' as const,
      workspace_id: workspaceId,
      scopes: ['full'],
    }));
    renderDetail({
      id: 'plugin-jtype',
      provider: 'jtype',
      status: 'action_required',
      scopes: ['full'],
      token_set: true,
    }, { setProjectPluginWorkspace });

    const picker = await screen.findByRole('combobox', { name: /Bound workspace/ });
    expect(screen.queryByRole('button', { name: 'Enable' })).toBeNull();
    fireEvent.change(picker, { target: { value: 'ws-1' } });
    await waitFor(() => expect(setProjectPluginWorkspace).toHaveBeenCalledWith('plugin-jtype', 'ws-1'));
  });

  it('requires the exact typed confirmation after showing uninstall impact', async () => {
    const uninstallProjectPlugin = vi.fn(async () => undefined);
    renderDetail({
      id: 'plugin-jtype',
      provider: 'jtype',
      status: 'enabled',
      workspace_id: 'ws-1',
      scopes: ['full'],
      token_set: true,
    }, {
      getProjectPluginImpact: async () => ({ services: 2, automations: 3 }),
      uninstallProjectPlugin,
    });

    fireEvent.click((await screen.findAllByRole('button', { name: 'Uninstall Plugin' }))[0]!);
    expect(await screen.findByText((_content, node) =>
      node?.tagName === 'P' &&
      node.textContent?.includes('2 Services and 3 Automations will be permanently deleted.') === true,
    )).toBeTruthy();
    const confirm = screen.getAllByRole('button', { name: 'Uninstall Plugin' }).at(-1)!;
    expect(confirm.hasAttribute('disabled')).toBe(true);
    fireEvent.change(screen.getByLabelText('Type UNINSTALL to confirm'), { target: { value: 'UNINSTALL' } });
    fireEvent.click(confirm);
    await waitFor(() => expect(uninstallProjectPlugin).toHaveBeenCalledWith('plugin-jtype', false));
  });

  it('localizes Plugin details and shows a human-readable Automation trigger', async () => {
    await i18n.changeLanguage('zh-Hans');
    try {
      renderDetail({
        id: 'plugin-jtype',
        provider: 'jtype',
        status: 'enabled',
        workspace_id: 'ws-1',
        external_account_id: 'account-1',
        scopes: ['full'],
        token_set: true,
      }, {
        getProject: async () => ({
          id: 'p1', name: 'Demo', created_at: '', role: 'owner' as const,
          services: [{
            id: 'svc-1', project_id: 'p1', name: 'Docs', repo_kind: 'provider', provider: 'jtype',
            default_branch: 'main', git_mode: 'readonly', created_at: '',
          }],
        }),
        listProjectAutomations: async () => [{
          automation: {
            id: 'a1', service_id: 'svc-1', name: 'Daily docs', trigger_kind: 'cron' as const,
            prompt_template: 'Review', enabled: true, ignore_jcode: true, created_at: '', updated_at: '',
          },
          cron: { cron_expr: '0 9 * * 1-5' },
        }],
      });

      expect(await screen.findByText('关联服务')).toBeTruthy();
      expect(screen.getByText('定时任务')).toBeTruthy();
      expect(screen.getByText('稳定 ID：account-1')).toBeTruthy();
      expect(screen.queryByText('Services')).toBeNull();
    } finally {
      await i18n.changeLanguage('en');
    }
  });
});
