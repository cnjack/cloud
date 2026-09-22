import { fireEvent, render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { setLocale } from '../i18n';
import { ToastProvider } from '../components/Toast';
import { RemoteComposer } from './RemoteComposer';

const refreshModels = vi.hoisted(() => vi.fn());
vi.mock('@jcloud/device-ui', () => ({
  useDeviceComposer: () => ({
    host: { projectPath: '', refreshModels }, runtime: {}, isSendLocked: false,
    releaseNewSessionLock: vi.fn(),
  }),
  usePendingNewSession: () => ({ pending: null, issue: null, found: null, markSent: vi.fn(), clear: vi.fn() }),
  DevicePairingGate: ({ children }: { children: ReactNode }) => children,
  DevicePairingCard: () => null,
  DevicePairingApprovals: () => null,
}));
vi.mock('../components/DeviceModelNotice', () => ({ DeviceModelNotice: () => null }));
vi.mock('jcode-ui', () => ({ RuntimeProvider: ({ children }: { children: ReactNode }) => children }));
vi.mock('jcode-ui/product', () => ({ ChatInput: ({ sendDisabled }: { sendDisabled: boolean }) => <button disabled={sendDisabled}>Send</button> }));

beforeEach(async () => { vi.clearAllMocks(); await setLocale('zh-Hans'); });
afterEach(async () => { await setLocale('en'); });

it('explains missing device workspace in the real locale and offers recovery without sending', () => {
  render(<MemoryRouter><ToastProvider><RemoteComposer device={{ id: 'legacy', name: 'Mac', online: true, e2ee: true }} /></ToastProvider></MemoryRouter>);
  expect(screen.getByRole('alert').textContent).toContain('无法读取当前文件夹。请重试、选择文件夹，或升级设备端 jcode 后使用 Chat。');
  expect(screen.getByRole('link', { name: 'Remote 设置' }).getAttribute('href')).toBe('/devices/guide');
  expect((screen.getByRole('button', { name: 'Send' }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(screen.getByRole('button', { name: '重试' }));
  expect(refreshModels).toHaveBeenCalledOnce();
});
