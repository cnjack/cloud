import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { DeviceGuidePage } from './DeviceGuidePage';

vi.mock('@jcloud/device-ui', async (importOriginal) => {
  const original = await importOriginal<typeof import('@jcloud/device-ui')>();
  return { ...original, useDevices: () => ({ data: [{ id: 'device-1', name: 'dev-mbp-01', online: true, platform: 'darwin' }] }) };
});

describe('Remote onboarding', () => {
  it('is setup-only and sends a one-time code to the real authorization flow', () => {
    render(<MemoryRouter initialEntries={['/devices/guide']}><Routes><Route path="/devices/guide" element={<DeviceGuidePage />} /><Route path="/device" element={<div data-testid="authorization" />} /></Routes></MemoryRouter>);
    expect(screen.getByRole('heading', { name: 'Connect a jcode device' })).toBeTruthy();
    expect(screen.queryByText('Device management')).toBeNull();
    expect(screen.getByText(COMMAND)).toBeTruthy();
    expect(screen.getByRole('link', { name: /dev-mbp-01/ }).getAttribute('href')).toBe('/?remote=device-1');
    fireEvent.change(screen.getByLabelText('Enter the code shown by the CLI'), { target: { value: 'JCDX-4H7Q' } });
    fireEvent.click(screen.getByRole('button', { name: /Continue authorization/ }));
    expect(screen.getByTestId('authorization')).toBeTruthy();
  });
});

const COMMAND = 'jcode login --cloud https://cloud.j-code.net';
