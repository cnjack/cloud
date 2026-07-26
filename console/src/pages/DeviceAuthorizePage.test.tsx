import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/client';
import { DeviceAuthorizePage } from './DeviceAuthorizePage';

const api = vi.hoisted(() => ({
  authorize: vi.fn(),
  state: vi.fn(),
}));

vi.mock('../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api/client')>()),
  postDeviceAuthorize: api.authorize,
  getDeviceAuthorizeState: api.state,
}));

vi.mock('../auth/AuthProvider', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../auth/AuthProvider')>()),
  useAuth: () => ({ getToken: () => 'test-token' }),
}));

function renderPage(path = '/device') {
  window.history.pushState({}, '', path);
  return render(
    <MemoryRouter initialEntries={[path]}>
      <DeviceAuthorizePage />
    </MemoryRouter>,
  );
}

describe('DeviceAuthorizePage', () => {
  beforeEach(() => {
    api.authorize.mockReset();
    api.state.mockReset();
  });

  it('uses the standalone security layout for the device-code flow', () => {
    renderPage();
    expect(screen.getByTestId('device-authorization-page')).toBeTruthy();
    expect(screen.getByText('Authorize a device')).toBeTruthy();
    expect(screen.getByText('Only approve a request you started')).toBeTruthy();
  });

  it('shows expired or unknown codes as an explicit terminal state', async () => {
    api.authorize.mockRejectedValue(new ApiError(404, 'not found', {
      error: { code: 'not_found', message: 'not found' },
    }));
    renderPage('/device?user_code=HTTP-1921');

    await waitFor(() => expect(screen.getByTestId('device-confirm')).toBeTruthy());
    fireEvent.click(screen.getByTestId('device-approve'));

    expect(await screen.findByTestId('device-unavailable')).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'This sign-in request is no longer available' })).toBeTruthy();
    expect(screen.getByText(/No pending sign-in matches that code/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Try another code' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Back to devices' })).toBeTruthy();
    expect(screen.queryByTestId('device-approve')).toBeNull();
  });
});
