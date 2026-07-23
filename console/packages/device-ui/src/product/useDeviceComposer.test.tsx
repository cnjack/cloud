/*
 * useDeviceComposer.test.tsx — the cloud ProductComposerHost + DeviceChatRuntime
 * pair against a fake DeviceApi (M14). Covers the send pipeline end to end:
 * goal_armed priority, compose extras assembly (model/mode/effort/project),
 * the type-ahead queue drain, and approval-vocabulary mapping.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useEffect, type ReactNode } from 'react';
import { act, render, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { DeviceApiProvider } from '../api/DeviceApiProvider';
import type { Device, DeviceApi, SendMessageExtras } from '../api/devices';
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
  extras?: SendMessageExtras;
}

function makeFakeApi() {
  const sends: SendCall[] = [];
  const approvals: { approvalId: string; decision: string }[] = [];
  const browsed: (string | undefined)[] = [];
  const api: Partial<DeviceApi> = {
    sendMessage: async (_d, sessionId, text, mode, extras) => {
      sends.push({ sessionId, text, mode, extras });
      return { command_id: 'cmd-1', session_id: sessionId === 'new' ? null : sessionId };
    },
    stopSession: async () => {},
    respondApproval: async (_d, _s, approvalId, decision) => {
      approvals.push({ approvalId, decision });
    },
    browseFolders: async (_d, path) => {
      browsed.push(path);
      return { current: path ?? '/home/jack', folders: [{ name: 'work', path: '/home/jack/work' }] };
    },
  };
  return { api: api as DeviceApi, sends, approvals, browsed };
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

type ComposerState = ReturnType<typeof useDeviceComposer>;

function KeyedComposerHarness({
  deviceId,
  report,
}: {
  deviceId: string;
  report: (composer: ComposerState) => void;
}) {
  const composer = useDeviceComposer({ deviceId, sessionId: 'new', device: DEVICE });
  useEffect(() => report(composer), [composer, report]);
  return null;
}

beforeEach(() => {
  // Node ≥23 exposes a disabled global localStorage that shadows jsdom's.
  // Install a tiny per-test implementation so session preference behavior is
  // exercised exactly as it is in browsers and mobile webviews.
  const values = new Map<string, string>();
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
      clear: () => values.clear(),
      key: (index: number) => [...values.keys()][index] ?? null,
      get length() { return values.size; },
    } satisfies Storage,
  });
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

  it('browses folders on the desktop device', async () => {
    const { api, browsed } = makeFakeApi();
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'new', device: DEVICE }),
      { wrapper: wrapper(api) },
    );
    await expect(result.current.host.browseFolders('/home/jack')).resolves.toEqual({
      current: '/home/jack',
      folders: [{ name: 'work', path: '/home/jack/work' }],
    });
    expect(browsed).toEqual(['/home/jack']);
  });

  it('accepts only one welcome creation command until a terminal outcome releases it', async () => {
    const { api, sends } = makeFakeApi();
    const onSent = vi.fn();
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'new', device: DEVICE, onSent }),
      { wrapper: wrapper(api) },
    );

    act(() => {
      result.current.runtime.actions.sendMessage('first');
      result.current.runtime.actions.sendMessage('second');
    });

    expect(sends.map((send) => send.text)).toEqual(['first']);
    expect(result.current.isSendLocked).toBe(true);
    await waitFor(() => expect(onSent).toHaveBeenCalledWith(expect.objectContaining({
      commandId: 'cmd-1',
      sessionId: 'new',
      text: 'first',
    })));
    act(() => result.current.releaseNewSessionLock());
    expect(result.current.isSendLocked).toBe(false);
  });

  it('does not carry a new-session lock across a device route switch', async () => {
    let latest: ComposerState | null = null;
    let rejectA: ((error: Error) => void) | null = null;
    const sends: string[] = [];
    const api = {
      sendMessage: async (deviceId: string) => {
        sends.push(deviceId);
        if (deviceId === 'device-a') {
          return new Promise<never>((_resolve, reject) => {
            rejectA = reject;
          });
        }
        return { command_id: 'command-b', session_id: null };
      },
      stopSession: async () => {},
      respondApproval: async () => {},
      browseFolders: async () => ({ current: '/', folders: [] }),
    } as unknown as DeviceApi;
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const report = (composer: ComposerState) => { latest = composer; };
    const renderTree = (deviceId: string) => (
      <QueryClientProvider client={qc}>
        <DeviceApiProvider api={api}>
          <KeyedComposerHarness key={deviceId} deviceId={deviceId} report={report} />
        </DeviceApiProvider>
      </QueryClientProvider>
    );
    const screen = render(renderTree('device-a'));
    await waitFor(() => expect(latest).not.toBeNull());
    act(() => latest!.runtime.actions.sendMessage('A'));
    expect(latest!.isSendLocked).toBe(true);
    await waitFor(() => expect(sends).toEqual(['device-a']));

    screen.rerender(renderTree('device-b'));
    await waitFor(() => expect(latest!.isSendLocked).toBe(false));
    act(() => latest!.runtime.actions.sendMessage('B'));
    expect(sends).toEqual(['device-a', 'device-b']);
    expect(latest!.isSendLocked).toBe(true);

    await act(async () => {
      rejectA?.(new Error('late A failure'));
      await Promise.resolve();
    });
    expect(latest!.isSendLocked).toBe(true);
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
        { data: 'aGk=', media_type: 'image/png' },
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
        images: [{ data: 'aGk=', media_type: 'image/png' }],
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

  it('restores the applied session mode and does not resend it after remount', async () => {
    const { api, sends } = makeFakeApi();
    const first = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-restore', device: DEVICE }),
      { wrapper: wrapper(api) },
    );
    act(() => first.result.current.host.selectMode('auto'));
    await act(async () => {
      first.result.current.runtime.actions.sendMessage('first');
    });
    expect(sends[0]!.mode).toBe('auto');
    first.unmount();

    const second = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-restore', device: DEVICE }),
      { wrapper: wrapper(api) },
    );
    expect(second.result.current.host.mode).toBe('auto');
    await act(async () => {
      second.result.current.runtime.actions.sendMessage('second');
    });
    expect(sends[1]!.mode).toBeUndefined();
  });

  it('restores mode and model from durable session events and hides unchanged repeats', async () => {
    const { api } = makeFakeApi();
    const streamState = {
      ...initialDeviceSessionState(),
      events: [
        { seq: 1, ts: '2026-01-01T00:00:00Z', kind: 'model_changed', payload: { data: { provider: 'anthropic', model: 'claude-opus-4' } } },
        { seq: 2, ts: '2026-01-01T00:00:01Z', kind: 'mode_changed', payload: { data: { mode: 'auto' } } },
        { seq: 3, ts: '2026-01-01T00:00:02Z', kind: 'model_changed', payload: { data: { provider: 'anthropic', model: 'claude-opus-4' } } },
        { seq: 4, ts: '2026-01-01T00:00:03Z', kind: 'mode_changed', payload: { data: { mode: 'auto' } } },
      ],
    };
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-events', device: DEVICE, streamState }),
      { wrapper: wrapper(api) },
    );
    await act(async () => {});
    expect(result.current.host.mode).toBe('auto');
    expect(result.current.host.providerName).toBe('anthropic');
    expect(result.current.host.modelName).toBe('claude-opus-4');
    const systemRows = result.current.runtime.getState().items.filter(
      (item) => item.kind === 'message' && item.data.role === 'system',
    );
    expect(systemRows).toHaveLength(2);
  });

  it('keeps meaningful goal updates but hides the cleared-goal noise row', async () => {
    const { api } = makeFakeApi();
    const streamState = {
      ...initialDeviceSessionState(),
      events: [
        { seq: 1, ts: '2026-01-01T00:00:00Z', kind: 'goal_update', payload: { data: { objective: 'ship it', status: 'active' } } },
        { seq: 2, ts: '2026-01-01T00:00:01Z', kind: 'goal_update', payload: { data: null } },
      ],
    };
    const { result } = renderHook(
      () => useDeviceComposer({ deviceId: 'dev-1', sessionId: 'sess-goal', device: DEVICE, streamState }),
      { wrapper: wrapper(api) },
    );
    await act(async () => {});
    const systemRows = result.current.runtime.getState().items.filter(
      (item) => item.kind === 'message' && item.data.role === 'system',
    );
    expect(systemRows).toHaveLength(1);
    expect(systemRows[0]?.kind === 'message' && systemRows[0].data.content).toContain('ship it');
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
