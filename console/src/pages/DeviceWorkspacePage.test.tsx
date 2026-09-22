import { fireEvent, render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { DeviceListPage, DeviceWorkspacePage } from './DeviceWorkspacePage';
import { DeviceSessionPage } from './DeviceSessionPage';

const state = vi.hoisted(() => ({
  paired: false,
  devices: { data: [] as Array<{ id: string; name: string; online: boolean; e2ee: boolean }>, isPending: false, isError: false, error: undefined as unknown, refetch: vi.fn() },
  sessions: vi.fn(), stream: vi.fn(), composer: vi.fn(),
}));
vi.mock('@jcloud/device-ui', async (original) => ({
  ...await original<typeof import('@jcloud/device-ui')>(),
  useDevices: () => state.devices,
  useDeviceSessions: () => { state.sessions(); return { data: [{ session_id: 's1', status: 'idle', meta: { title: 'Encrypted title' } }] }; },
  useDeviceSessionStream: () => { state.stream(); return { state: { events: [], finalizedText: [], streamingText: '', agentRunning: false }, online: false, phase: 'connected' }; },
  useDeviceComposer: () => { state.composer(); return { host: {}, runtime: {} }; },
  DevicePairingGate: ({ children }: { children: ReactNode }) => state.paired ? children : <div>Pair this browser</div>,
  DevicePairingCard: () => null,
}));
vi.mock('./DeviceSessionInspector', () => ({ DeviceSessionInspector: () => <aside>Session inspector</aside> }));
vi.mock('../work-home/RemoteComposer', () => ({ RemoteComposer: ({ device }: { device: { online: boolean } }) => <fieldset disabled={!device.online}><button>Send remote task</button></fieldset> }));
vi.mock('jcode-ui', () => ({ RuntimeProvider: ({ children }: { children: ReactNode }) => children, Thread: () => <div>Private history</div> }));
vi.mock('jcode-ui/product', () => ({ ChatInput: () => <button>Send message</button> }));

function show(path: string) {
  return render(<MemoryRouter initialEntries={[path]}><Routes>
    <Route path="/devices" element={<DeviceListPage />} />
    <Route path="/devices/:deviceId" element={<DeviceWorkspacePage />} />
    <Route path="/devices/:deviceId/sessions/:sessionId" element={<DeviceSessionPage />} />
  </Routes></MemoryRouter>);
}

beforeEach(() => {
  state.paired = false;
  state.devices.data = [{ id: 'd1', name: 'Work Mac', online: true, e2ee: true }];
  state.devices.isPending = false; state.devices.isError = false; state.devices.error = undefined;
  vi.clearAllMocks();
});

describe('Device workspace routing and privacy', () => {
  it('searches real returned device records and opens the matching workspace', () => {
    show('/devices');
    const search = screen.getByRole('searchbox', { name: 'Search devices' });
    fireEvent.change(search, { target: { value: 'unmatched' } });
    expect(screen.queryByRole('link', { name: /Work Mac/ })).toBeNull();
    fireEvent.change(search, { target: { value: 'work' } });
    fireEvent.click(screen.getByRole('link', { name: /Work Mac/ }));
    expect(screen.getByTestId('device-workspace')).toBeTruthy();
    expect(screen.getByText('Pair this browser')).toBeTruthy();
  });

  it.each(['/devices/d1', '/devices/d1/sessions/s1'])('does not query or render private content before pairing at %s', (path) => {
    show(path);
    expect(state.sessions).not.toHaveBeenCalled();
    expect(state.stream).not.toHaveBeenCalled();
    expect(state.composer).not.toHaveBeenCalled();
    expect(screen.queryByText('Encrypted title')).toBeNull();
    expect(screen.queryByText('Private history')).toBeNull();
  });

  it('waits for the device identity instead of bypassing pairing while loading', () => {
    state.devices.data = []; state.devices.isPending = true;
    show('/devices/d1/sessions/s1');
    expect(state.sessions).not.toHaveBeenCalled();
    expect(state.stream).not.toHaveBeenCalled();
    expect(screen.queryByText('Private history')).toBeNull();
  });

  it('does not open a stream or composer for a nonexistent paired session', () => {
    state.paired = true;
    show('/devices/d1/sessions/missing');
    expect(screen.getByText('Session not found')).toBeTruthy();
    expect(state.stream).not.toHaveBeenCalled();
    expect(state.composer).not.toHaveBeenCalled();
  });

  it('shows history but disables sending on a paired offline device', () => {
    state.paired = true; state.devices.data[0]!.online = false;
    show('/devices/d1/sessions/s1');
    expect(screen.getByRole('heading', { name: 'Encrypted title' })).toBeTruthy();
    expect(screen.getByText('Private history')).toBeTruthy();
    expect(screen.getByTestId('offline-banner')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Send message' }).closest('fieldset')?.disabled).toBe(true);
  });

  it('fails visibly with a retry instead of showing an empty device list on errors', () => {
    state.devices.isError = true; state.devices.error = new Error('Device service unavailable');
    show('/devices');
    expect(screen.getByRole('alert').textContent).toContain('Device service unavailable');
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(state.devices.refetch).toHaveBeenCalledOnce();
    expect(screen.queryByText('No devices connected')).toBeNull();
  });
});
