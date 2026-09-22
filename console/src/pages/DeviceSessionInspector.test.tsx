import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { DeviceSessionInspector } from './DeviceSessionInspector';

const state = vi.hoisted(() => ({ inspect: vi.fn(), deliver: vi.fn(), prepare: vi.fn() }));
vi.mock('@jcloud/device-ui', async (original) => ({ ...await original<typeof import('@jcloud/device-ui')>(), useDeviceApi: () => ({ inspectWorkspace: state.inspect, createWorkspaceDraftPR: state.deliver, prepareWorkspaceDraftPR: state.prepare }) }));
const device = { id: 'd1', name: 'Work Mac', online: true, capabilities: { workspace_actions: ['changes', 'draft_pr'] } };
const session = { session_id: 's1', status: 'idle', updated_at: '', meta: { project: '/repo', model: 'model-a' } };
function show(online = true, supported = true) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MemoryRouter><DeviceSessionInspector device={supported ? device : { ...device, capabilities: {} }} session={session} online={online} /></MemoryRouter></QueryClientProvider>);
}
beforeEach(() => {
  state.prepare.mockReset().mockImplementation(() => state.inspect());
  state.deliver.mockReset().mockResolvedValue({ url: 'https://github.com/owner/repo/pull/12', branch: 'jcode/cloud-reviewed', base: 'main' });
  state.inspect.mockReset().mockResolvedValue({ session_id: 's1', branch: 'task-branch', revision: 'reviewed-revision', base_sha: 'base-sha', base_branch: 'main', remote_url: 'https://github.com/owner/repo', files: [{ path: 'src/input.ts', patch: '--- a/input\n+++ b/input\n+fixed', additions: 1, deletions: 0 }], truncated: false });
});
describe('Remote session inspector', () => {
  it('opens the returned file patch and reports actual workspace details', async () => {
    show();
    fireEvent.click(await screen.findByRole('button', { name: /src\/input.ts/ }));
    expect(screen.getByRole('dialog').textContent).toContain('+fixed');
    fireEvent.keyDown(document, { key: 'Escape' });
    fireEvent.click(screen.getByRole('tab', { name: 'Details' }));
    expect(screen.getByText('task-branch')).toBeTruthy();
    expect(state.inspect).toHaveBeenCalledWith('d1', 's1');
  });
  it('requires a title and selected reviewed files before publishing the sealed revision', async () => {
    show();
    fireEvent.click(await screen.findByRole('button', { name: 'Create draft pull request' }));
    const publish = screen.getByRole('button', { name: 'Push branch and create draft' }) as HTMLButtonElement;
    expect(publish.disabled).toBe(true);
    fireEvent.change(await screen.findByLabelText('Pull request title'), { target: { value: 'Fix input' } });
    fireEvent.click(screen.getByRole('checkbox', { name: 'src/input.ts' }));
    expect(publish.disabled).toBe(false);
    fireEvent.click(publish);
    expect(await screen.findByRole('link', { name: 'Open pull request' })).toBeTruthy();
    expect(state.deliver).toHaveBeenCalledWith('d1', 's1', { repository_url: 'https://github.com/owner/repo', revision: 'reviewed-revision', base_sha: 'base-sha', paths: ['src/input.ts'], title: 'Fix input', body: '' });
  });
  it('does not request offline or unsupported devices and provides recovery', () => {
    const view = show(false);
    expect(state.inspect).not.toHaveBeenCalled();
    expect((screen.getByRole('button', { name: 'Refresh' }) as HTMLButtonElement).disabled).toBe(true);
    view.unmount(); show(true, false);
    expect(state.inspect).not.toHaveBeenCalled();
    expect(screen.getByRole('link', { name: 'Device setup' }).getAttribute('href')).toBe('/devices/guide');
  });
  it('shows a failed read and retries without fabricating an empty workspace', async () => {
    state.inspect.mockRejectedValueOnce(new Error('Workspace unavailable'));
    show();
    expect((await screen.findByRole('alert')).textContent).toContain('Workspace unavailable');
    expect(screen.queryByText('No uncommitted workspace changes.')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(state.inspect).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole('button', { name: /src\/input.ts/ })).toBeTruthy();
  });
});
