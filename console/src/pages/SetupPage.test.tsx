import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import { SetupPage } from './SetupPage';

function renderSetup(client: ApiClient) {
  return render(<ApiProvider client={client}><MemoryRouter><SetupPage /></MemoryRouter></ApiProvider>);
}

describe('SetupPage', () => {
  it('uses the current browser origin when setup has no stored public URL', async () => {
    const client = {
      getSetupStatus: async () => ({ setup_required: true, public_url: '', login_provider_count: 0 }),
    } as unknown as ApiClient;
    renderSetup(client);
    const input = await screen.findByLabelText(/Cloud public URL/);
    expect((input as HTMLInputElement).value).toBe(window.location.origin);
    expect(screen.getByText(`${window.location.origin}/auth/callback/github`)).toBeTruthy();
  });

  it('submits public URL and one encrypted-login-provider configuration together', async () => {
    const updateSetup = vi.fn(() => new Promise<never>(() => {}));
    const client = {
      getSetupStatus: async () => ({ setup_required: true, public_url: 'http://localhost:5173', login_provider_count: 0 }),
      updateSetup,
    } as unknown as ApiClient;
    renderSetup(client);
    await waitFor(() => expect(screen.getByTestId('first-visitor-setup')).toBeTruthy());
    fireEvent.change(screen.getByLabelText(/OAuth client ID/), { target: { value: 'client-id' } });
    fireEvent.change(screen.getByLabelText(/OAuth client secret/), { target: { value: 'client-secret' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save and continue' }));
    expect(updateSetup).toHaveBeenCalledWith(expect.objectContaining({
      public_url: 'http://localhost:5173',
      provider: expect.objectContaining({ provider: 'github', base_url: 'https://github.com', login_enabled: true, client_secret: 'client-secret' }),
    }));
  });
});
