/*
 * useDeviceComposer.test.tsx — the cloud ProductComposerHost + DeviceChatRuntime
 * pair against a fake DeviceApi (M14). Covers the send pipeline end to end:
 * goal_armed priority, compose extras assembly (model/mode/effort/project),
 * the type-ahead queue drain, and approval-vocabulary mapping.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { DeviceApiProvider } from '../api/DeviceApiProvider';
import type { Device, DeviceApi } from '../api/devices';
import { initialDeviceSessionState } from '../deviceview/sessionReducer';
import { useDeviceComposer } from './useDeviceComposer';

const CAPS: Device['capabilities'] = {
  projects: [{ path: '/home/jack/a', name: 'a' }],
  models: [{ provider: 'anthropic', id: 'claude-opus-4', label: 'Claude Opus 4' }],
  efforts: ['low', 'medium', 'high'],
  slash_commands: [{ slash: '/review', description: 'Review code', type: 'skill' }],
};

const DEVICE: Device = { id: 'dev-1', name: 'dev', online: true, capabilities: CAPS };

interface SendCall {
  sessionId: string;
  text: string;
  mode?: string;
  extras?: Record<string, unknown>;
}

function makeFakeApi() {
  const sends: SendCall[] = [];
  const approvals: { approvalId: string; decision: string }[] = [];
  const api: Partial<DeviceApi> = {
    sendMessage: async (_d, sessionId, text, mode, extras) => {
      sends.push({ sessionId, text, mode, extras });
      return { command_id: 'cmd-1', session_id: sessionId === 'new' ? null : sessionId };
    },
    stopSession: async () => {},
    respondApproval: async (_d, _s, approvalId, decision) => {
      approvals.push({ approvalId, decision });
    },
  };
  return { api: api as DeviceApi, sends, approvals };
}

function wrapper(api: DeviceApi) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={qc}>
        <DeviceApiProvider api={api}>{children}</DeviceApiProvider>
      </QueryClientProvider>
    );
  };
}

beforeEach(() => {
  // Node ≥23 exposes a global localStorage that shadows jsdom's; either may
  // be undefined here — the hook guards the same way.
  globalThis.localStorage?.clear();
});

describe('useDeviceComposer', () => {
  it('projects capabilities onto the host state model', () => {
    const { api } = makeFakeApi();
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-1', device: DEVICE }),
      { wrapper: wrapper(api) },
    );
    const host = result.current.host;
    expect(host.providers[0]!.models[0]).toMatchObject({ id: 'claude-opus-4', name: 'Claude Opus 4' });
    expect(host.slashCommands).toEqual([{ slash: '/review', description: 'Review code', type: 'skill' }]);
    expect(host.tasks).toEqual([{ uuid: '/home/jack/a', project: '/home/jack/a' }]);
    expect(host.imageSupport).toBe(true);
    expect(host.goalArmed).toBe(false);
    expect(host.projectPath).toBe('');
  });

  it('degrades cleanly without capabilities (old connector)', () => {
    const { api } = makeFakeApi();
    const old: Device = { id: 'dev-1', name: 'dev', online: true, capabilities: null };
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-1', device: old }),
      { wrapper: wrapper(api) },
    );
    const host = result.current.host;
    expect(host.providers).toEqual([]);
    expect(host.slashCommands).toEqual([]);
    expect(host.tasks).toEqual([]);
    expect(host.imageSupport).toBe(false);
  });

  it('sends an armed goal as {goal_armed: true} alone — goal beats every compose field', async () => {
    const { api, sends } = makeFakeApi();
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-1', device: DEVICE }),
      { wrapper: wrapper(api) },
    );
    act(() => {
      result.current.host.selectModel('anthropic', 'claude-opus-4');
      result.current.host.selectMode('plan');
      result.current.host.setGoalArmed(true);
    });
    await act(async () => {
      result.current.runtime.actions.sendMessage('make tests green');
    });
    expect(sends).toHaveLength(1);
    expect(sends[0]).toEqual({
      sessionId: 'sess-1',
      text: 'make tests green',
      mode: undefined,
      extras: { goal_armed: true },
    });
    // The armed flag is consumed by the send.
    expect(result.current.host.goalArmed).toBe(false);
  });

  it('assembles mode + model/effort/project extras from the compose state', async () => {
    const { api, sends } = makeFakeApi();
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-1', device: DEVICE }),
      { wrapper: wrapper(api) },
    );
    act(() => {
      result.current.host.selectModel('anthropic', 'claude-opus-4');
      result.current.host.setEffort('anthropic', 'claude-opus-4', 'high');
      result.current.host.selectMode('plan');
    });
    await act(async () => {
      await result.current.host.switchWorkspace('/home/jack/a');
    });
    await act(async () => {
      result.current.runtime.actions.sendMessage('hello', [
        { data: 'aGk=', media_type: 'image/png', name: 'hi.png' },
      ]);
    });
    expect(sends[0]).toEqual({
      sessionId: 'sess-1',
      text: 'hello',
      mode: 'plan',
      extras: {
        model: { provider: 'anthropic', id: 'claude-opus-4' },
        effort: 'high',
        project_path: '/home/jack/a',
        images: [{ data: 'aGk=', media_type: 'image/png', name: 'hi.png' }],
      },
    });
  });

  it('omits mode until the user explicitly picks one (device default preserved)', async () => {
    const { api, sends } = makeFakeApi();
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-1', device: DEVICE }),
      { wrapper: wrapper(api) },
    );
    await act(async () => {
      result.current.runtime.actions.sendMessage('hi');
    });
    expect(sends[0]!.mode).toBeUndefined();
    expect(sends[0]!.extras).toBeUndefined();
  });

  it('caps the mode picker at auto (M20): full_access is not offered nor selectable', async () => {
    const { api, sends } = makeFakeApi();
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-1', device: DEVICE }),
      { wrapper: wrapper(api) },
    );
    // The composer dropdown is driven by host.allowedModes (ChatInput filters
    // MODE_DEFS with it) — full_access must not appear.
    expect(result.current.host.allowedModes).toEqual(['approval', 'plan', 'auto']);
    // A caller bypassing the picker is ignored too; the send carries no mode
    // (and the device connector would ack mode_not_allowed_for_cloud anyway).
    act(() => {
      result.current.host.selectMode('full_access');
    });
    await act(async () => {
      result.current.runtime.actions.sendMessage('hi');
    });
    expect(sends[0]!.mode).toBeUndefined();
    expect(result.current.host.mode).toBe('approval');
    // Allowed modes still stick.
    act(() => {
      result.current.host.selectMode('auto');
    });
    await act(async () => {
      result.current.runtime.actions.sendMessage('hi again');
    });
    expect(sends[1]!.mode).toBe('auto');
  });

  it('drains the type-ahead queue when the turn ends', async () => {
    const { api, sends } = makeFakeApi();
    const running = { ...initialDeviceSessionState(), agentRunning: true };
    const idle = { ...initialDeviceSessionState(), agentRunning: false };
    const { result, rerender } = renderHook(
      ({ streamState }) =>
        useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-1', device: DEVICE, streamState }),
      { wrapper: wrapper(api), initialProps: { streamState: running } },
    );
    act(() => {
      result.current.runtime.actions.enqueueMessage('follow up');
    });
    expect(result.current.runtime.getState().queued).toHaveLength(1);
    rerender({ streamState: idle });
    await act(async () => {});
    expect(result.current.runtime.getState().queued).toHaveLength(0);
    expect(sends.map((s) => s.text)).toEqual(['follow up']);
  });

  it('maps approval decisions onto the relay vocabulary', async () => {
    const { api, approvals } = makeFakeApi();
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-1', device: DEVICE }),
      { wrapper: wrapper(api) },
    );
    await act(async () => {
      result.current.runtime.actions.resolveApproval('a1', true);
      result.current.runtime.actions.resolveApproval('a2', true, true);
      result.current.runtime.actions.resolveApproval('a3', false);
    });
    expect(approvals).toEqual([
      { approvalId: 'a1', decision: 'approve' },
      { approvalId: 'a2', decision: 'approve_all' },
      { approvalId: 'a3', decision: 'deny' },
    ]);
  });

  it('appends a local system row (and onError) when a send fails', async () => {
    const { api } = makeFakeApi();
    api.sendMessage = vi.fn(async () => {
      throw new Error('device_offline');
    });
    const onError = vi.fn();
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-1', device: DEVICE, onError }),
      { wrapper: wrapper(api) },
    );
    await act(async () => {
      result.current.runtime.actions.sendMessage('hi');
    });
    await act(async () => {});
    const items = result.current.runtime.getState().items;
    const last = items[items.length - 1];
    expect(last!.kind).toBe('message');
    expect(last!.kind === 'message' && last!.data.role).toBe('system');
    expect(onError).toHaveBeenCalledOnce();
  });
});
