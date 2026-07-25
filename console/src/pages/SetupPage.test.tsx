import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ApiProvider } from '../api/ApiProvider';
import type { ApiClient } from '../api/client';
import { SetupPage } from './SetupPage';

describe('SetupPage', () => {
  it('submits public URL and one encrypted-login-provider configuration together', async () => {
    const updateSetup = vi.fn(() => new Promise<never>(() => {}));
    const client = {
      getSetupStatus: async () => ({ setup_required: true, public_url: 'http://localhost:5173', login_provider_count: 0 }),
      updateSetup,
    } as unknown as ApiClient;
    render(<ApiProvider client={client}><SetupPage /></ApiProvider>);
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
